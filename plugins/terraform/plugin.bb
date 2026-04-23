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
;;   emit_state     - run `terraform show -json` and `terraform output -json`
;;                    after apply (or after plan in plan-only mode), capturing
;;                    state.json and outputs.json as CAS outputs so downstream
;;                    targets (e.g. pudl) can consume them. Default: true.
;;   binary         - terraform-compatible CLI to invoke. If unset, prefers
;;                    `tofu` (OpenTofu) when on PATH, else falls back to
;;                    `terraform`. Accepts any command name or absolute path.

(require '[cheshire.core :as json]
         '[clojure.string :as str]
         '[babashka.fs :as fs]
         '[babashka.process :as process])

(defn resolve-bin
  "Resolve which terraform-compatible CLI to invoke. Config `binary`
  takes precedence; otherwise prefer `tofu` on PATH, else `terraform`."
  [config]
  (or (get config "binary")
      (when (fs/which "tofu") "tofu")
      "terraform"))

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
                       "parallelism"    {"type" "integer"}
                       "emit_state"     {"type" "boolean" "default" true}
                       "binary"         {"type" "string"}}})

(defn build-init-cmd
  "Build terraform init command with backend config flags."
  [config]
  (let [cmd [(resolve-bin config) "init" "-input=false" "-no-color"]]
    (if-let [bc (get config "backend_config")]
      (into cmd (map (fn [[k v]] (str "-backend-config=" k "=" v)) bc))
      cmd)))

(defn build-plan-cmd
  "Build terraform plan command."
  [config]
  (cond-> [(resolve-bin config) "plan" "-input=false" "-no-color" "-out=tfplan"]
    (get config "var_file")
    (conj (str "-var-file=" (get config "var_file")))
    (get config "parallelism")
    (conj (str "-parallelism=" (get config "parallelism")))))

(defn build-apply-cmd
  "Build terraform apply command."
  [config]
  (cond-> [(resolve-bin config) "apply" "-input=false" "-no-color" "-auto-approve" "tfplan"]
    (get config "parallelism")
    (conj (str "-parallelism=" (get config "parallelism")))))

(defn handle-plan [req]
  (let [target   (get req "target")
        tgt-name (get target "name")
        sources  (get target "sources" [])
        config   (get target "config" {})
        dir      (get config "dir" ".")
        auto?    (get config "auto_approve" true)
        emit?    (get config "emit_state" true)

        ;; Build input map from declared sources (.tf files)
        inputs (into {} (map (fn [s] [s s]) sources))

        ;; Init action
        init-action {"id"         "init"
                     "command"    (build-init-cmd config)
                     "inputs"     {}
                     "outputs"    []
                     "depends_on" []
                     "network"    true
                     "impure"     true
                     "work_dir"   dir}

        ;; Plan action
        plan-action {"id"         "plan"
                     "command"    (build-plan-cmd config)
                     "inputs"     inputs
                     "outputs"    [(str dir "/tfplan")]
                     "depends_on" ["init"]
                     "network"    true
                     "impure"     true
                     "work_dir"   dir}

        ;; Apply action (only when auto_approve is true)
        apply-action {"id"         "apply"
                      "command"    (build-apply-cmd config)
                      "inputs"     {}
                      "outputs"    []
                      "depends_on" ["plan"]
                       "network"    true
                      "impure"     true
                      "work_dir"   dir}

        ;; Show action: capture post-apply (or post-plan, if plan-only)
        ;; state + outputs as JSON. Wrapped in `sh -c` so we can redirect
        ;; both commands' stdout into the declared output files in one
        ;; shot. `terraform output -json` prints "{}" when no outputs
        ;; are declared, so the file is always present for downstream
        ;; targets to read.
        show-depends (if auto? ["apply"] ["plan"])
        bin          (resolve-bin config)
        show-action  {"id"         "show"
                      "command"    ["sh" "-c"
                                    (str bin " show -json > state.json && "
                                         bin " output -json > outputs.json")]
                      "inputs"     {}
                      "outputs"    [(str dir "/state.json")
                                    (str dir "/outputs.json")]
                      "depends_on" show-depends
                       "network"    true
                      "impure"     true
                      "work_dir"   dir}

        actions (cond-> [init-action plan-action]
                  auto?     (conj apply-action)
                  emit?     (conj show-action))

        declared (if emit?
                   {"terraform_state"   (str dir "/state.json")
                    "terraform_outputs" (str dir "/outputs.json")}
                   {})]

    {"actions"          actions
     "declared_outputs" declared}))

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
      (let [plan-cmd (cond-> [(resolve-bin config) "plan" "-input=false" "-no-color" "-detailed-exitcode"]
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
