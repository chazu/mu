#!/usr/bin/env bb

;; Terraform convergence plugin for mu.
;;
;; Converges Terraform-managed infrastructure via terraform init/plan/apply.
;; Observes drift via terraform plan -detailed-exitcode. All actions are
;; impure (side effects on infrastructure) and require network access.
;;
;; Config options:
;;   dir            - terraform working directory (default: ".")
;;   var_file       - path to .tfvars file (optional)
;;   backend_config - map of backend config key=value pairs (optional)
;;   auto_approve   - include apply step (default: true)
;;                    when false, only init + plan are emitted (plan-only mode)
;;   parallelism    - max concurrent terraform operations (optional)

(require '[cheshire.core :as json]
         '[clojure.string :as str]
         '[babashka.process :as process])

(defn handle-discover []
  {"name"             "terraform"
   "version"          "0.1.0"
   "protocol_version" 1
   "capabilities"     ["discover" "plan" "observe"]
   "consumes"         ["source:terraform" "source:hcl"]
   "produces"         ["terraform_state"]
   "config_schema"    {"dir"            {"type" "string"  "default" "."}
                       "var_file"       {"type" "string"}
                       "backend_config" {"type" "object"}
                       "auto_approve"   {"type" "boolean" "default" true}
                       "parallelism"    {"type" "integer"}}})

(defn build-init-cmd
  "Build terraform init command with backend config flags."
  [config]
  (let [cmd ["terraform" "init" "-input=false"]]
    (if-let [bc (get config "backend_config")]
      (into cmd (map (fn [[k v]] (str "-backend-config=" k "=" v)) bc))
      cmd)))

(defn build-plan-cmd
  "Build terraform plan command."
  [config]
  (cond-> ["terraform" "plan" "-input=false" "-out=tfplan"]
    (get config "var_file")
    (conj (str "-var-file=" (get config "var_file")))
    (get config "parallelism")
    (conj (str "-parallelism=" (get config "parallelism")))))

(defn build-apply-cmd
  "Build terraform apply command."
  [config]
  (cond-> ["terraform" "apply" "-input=false" "-auto-approve" "tfplan"]
    (get config "parallelism")
    (conj (str "-parallelism=" (get config "parallelism")))))

(defn handle-plan [req]
  (let [target   (get req "target")
        tgt-name (get target "name")
        sources  (get target "sources" [])
        config   (get target "config" {})
        dir      (get config "dir" ".")
        auto?    (get config "auto_approve" true)

        ;; Build input map from declared sources (.tf files)
        inputs (into {} (map (fn [s] [s s]) sources))

        ;; Init action
        init-action {"id"         "init"
                     "command"    (build-init-cmd config)
                     "inputs"     {}
                     "outputs"    []
                     "depends_on" []
                     "env"        {}
                     "network"    true
                     "impure"     true
                     "work_dir"   dir}

        ;; Plan action
        plan-action {"id"         "plan"
                     "command"    (build-plan-cmd config)
                     "inputs"     inputs
                     "outputs"    [(str dir "/tfplan")]
                     "depends_on" ["init"]
                     "env"        {}
                     "network"    true
                     "impure"     true
                     "work_dir"   dir}

        ;; Apply action (only when auto_approve is true)
        apply-action {"id"         "apply"
                      "command"    (build-apply-cmd config)
                      "inputs"     {}
                      "outputs"    []
                      "depends_on" ["plan"]
                      "env"        {}
                      "network"    true
                      "impure"     true
                      "work_dir"   dir}

        actions (cond-> [init-action plan-action]
                  auto? (conj apply-action))]

    {"actions"          actions
     "declared_outputs" {}}))

(defn handle-observe [req]
  (let [target  (get req "target")
        config  (get target "config" {})
        dir     (get config "dir" ".")]

    (try
      ;; First run terraform init (quiet) to configure backend
      (let [init-result (process/sh (build-init-cmd config)
                                     {:dir dir})]
        (when (not= 0 (:exit init-result))
          (throw (ex-info "terraform init failed" {:output (:err init-result)}))))

      ;; Then run terraform plan -detailed-exitcode
      (let [plan-cmd (cond-> ["terraform" "plan" "-input=false" "-detailed-exitcode"]
                       (get config "var_file")
                       (conj (str "-var-file=" (get config "var_file"))))
            result   (process/sh plan-cmd {:dir dir})]
        (cond
          ;; Exit 0 = no changes
          (= 0 (:exit result))
          {"current" {"has_changes" false
                      "plan_output" (str/trim (str (:out result)))}}

          ;; Exit 2 = changes detected
          (= 2 (:exit result))
          {"current" {"has_changes" true
                      "plan_output" (str/trim (str (:out result)))}}

          ;; Exit 1 = error
          :else
          {"error" (str "terraform plan failed (exit " (:exit result) "): "
                        (:err result))}))

      (catch Exception e
        {"error" (str "terraform observe failed: " (.getMessage e))}))))

(defn handle-request [req]
  (case (get req "method")
    "discover" (handle-discover)
    "plan"     (handle-plan req)
    "observe"  (handle-observe req)
    {"error" (str "unknown method: " (get req "method"))}))

;; Main NDJSON loop
(loop []
  (when-let [line (read-line)]
    (let [req  (json/parse-string line)
          resp (handle-request req)]
      (println (json/generate-string resp))
      (flush)
      (recur))))
