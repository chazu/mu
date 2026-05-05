#!/usr/bin/env bb

;; Terraform convergence plugin for mu.
;;
;; Converges Terraform-managed infrastructure via terraform init/plan/apply.
;; Observes drift via terraform plan -detailed-exitcode. All actions are
;; impure (side effects on infrastructure) and require network access.
;;
;; Config options:
;;   dir                  - terraform working directory (default: ".")
;;   var_file             - path to .tfvars file (optional)
;;   backend_config       - map of backend config key=value pairs (optional)
;;   auto_approve         - include apply step (default: true)
;;                          when false, only init + plan are emitted (plan-only mode)
;;   parallelism          - max concurrent terraform operations (optional)
;;   emit_state           - run `terraform show -json` and `terraform output -json`
;;                          after apply (or after plan in plan-only mode), capturing
;;                          state.json and outputs.json as CAS outputs so downstream
;;                          targets (e.g. pudl) can consume them. Default: true.
;;   binary               - terraform-compatible CLI to invoke. If unset, prefers
;;                          `tofu` (OpenTofu) when on PATH, else falls back to
;;                          `terraform`. Accepts any command name or absolute path.
;;   sealed_output_outputs - map sealed_output NAME -> terraform output name.
;;                          After apply (or plan in plan-only mode), the
;;                          fetch-secrets action runs `terraform output -raw NAME`
;;                          for each entry and writes the value to
;;                          $MU_SEALED_OUT_DIR/NAME (0600). Use for sensitive
;;                          outputs (database passwords, generated tokens) that
;;                          should land in a secret backend instead of
;;                          outputs.json-in-CAS. Keys must match the target's
;;                          sealed_outputs map.
;;
;; Sealed inputs are forwarded to every action (init, plan, apply, show,
;; fetch-secrets). Use sealed_input_modes:file for cloud-credentials JSON
;; (GCP service-account, AWS web-identity tokens) and other multi-line
;; secrets — terraform reads the path via env var per its provider docs.

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
   "version"          "0.2.0"
   "protocol_version" 1
   "capabilities"     ["discover" "plan" "observe"]
   "consumes"         ["source:terraform" "source:hcl"]
   "produces"         ["terraform_state"]
   "config_schema"    {"dir"                   {"type" "string"  "default" "."}
                       "var_file"              {"type" "string"}
                       "backend_config"        {"type" "object"}
                       "auto_approve"          {"type" "boolean" "default" true}
                       "parallelism"           {"type" "integer"}
                       "emit_state"            {"type" "boolean" "default" true}
                       "binary"                {"type" "string"}
                       "sealed_output_outputs" {"type" "object"}}})

(defn shell-quote
  "Single-quote a string for safe inclusion in a bash command."
  [s]
  (str "'" (str/replace (str s) "'" "'\\''") "'"))

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

(defn build-fetch-secrets-cmd
  "Bash one-liner: for each NAME -> tf_output_name, run
   `terraform output -raw <tf_output>` and write to $MU_SEALED_OUT_DIR/NAME
   (chmod 0600). Errors immediately on any missing/failed output."
  [bin sealed-out-outputs]
  (let [pairs (sort-by key sealed-out-outputs)]
    (str "set -eu\n"
         "if [ -z \"${MU_SEALED_OUT_DIR:-}\" ]; then\n"
         "  echo 'terraform: sealed_output_outputs declared but MU_SEALED_OUT_DIR is unset' >&2\n"
         "  exit 1\n"
         "fi\n"
         (str/join
          (for [[name tf-out] pairs]
            (str bin " output -raw " (shell-quote tf-out)
                 " > \"$MU_SEALED_OUT_DIR/" name "\"\n"
                 "chmod 600 \"$MU_SEALED_OUT_DIR/" name "\"\n"))))))

(defn attach-sealed-inputs
  "Add sealed_inputs / sealed_input_modes to an action map when present."
  [action sealed sealed-modes]
  (cond-> action
    (seq sealed)        (assoc "sealed_inputs" sealed)
    (seq sealed-modes)  (assoc "sealed_input_modes" sealed-modes)))

(defn handle-plan [req]
  (let [target           (get req "target")
        tgt-name         (get target "name")
        sources          (get target "sources" [])
        config           (get target "config" {})
        dir              (get config "dir" ".")
        auto?            (get config "auto_approve" true)
        emit?            (get config "emit_state" true)
        sealed           (get target "sealed_inputs" {})
        sealed-modes     (get target "sealed_input_modes" {})
        sealed-out       (get target "sealed_outputs" {})
        sealed-out-modes (get target "sealed_output_modes" {})
        sealed-out-outs  (get config "sealed_output_outputs" {})
        out-keys         (set (keys sealed-out))
        out-out-keys     (set (keys sealed-out-outs))]

    (cond
      (and (seq sealed-out) (not= out-keys out-out-keys))
      {"error" (str "terraform: sealed_outputs keys " out-keys
                    " must match config.sealed_output_outputs keys " out-out-keys)}

      (and (seq sealed-out) (not auto?))
      {"error" "terraform: sealed_outputs require auto_approve:true (need apply to populate outputs)"}

      :else
      (let [;; Build input map from declared sources (.tf files)
            inputs (into {} (map (fn [s] [s s]) sources))

            init-action  (-> {"id"         "init"
                              "command"    (build-init-cmd config)
                              "inputs"     {}
                              "outputs"    []
                              "depends_on" []
                              "network"    true
                              "impure"     true
                              "work_dir"   dir}
                             (attach-sealed-inputs sealed sealed-modes))

            plan-action  (-> {"id"         "plan"
                              "command"    (build-plan-cmd config)
                              "inputs"     inputs
                              "outputs"    [(str dir "/tfplan")]
                              "depends_on" ["init"]
                              "network"    true
                              "impure"     true
                              "work_dir"   dir}
                             (attach-sealed-inputs sealed sealed-modes))

            apply-action (-> {"id"         "apply"
                              "command"    (build-apply-cmd config)
                              "inputs"     {}
                              "outputs"    []
                              "depends_on" ["plan"]
                              "network"    true
                              "impure"     true
                              "work_dir"   dir}
                             (attach-sealed-inputs sealed sealed-modes))

            show-depends (if auto? ["apply"] ["plan"])
            bin          (resolve-bin config)
            show-action  (-> {"id"         "show"
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
                             (attach-sealed-inputs sealed sealed-modes))

            ;; Fetch-secrets action: runs terraform output -raw per name
            ;; into $MU_SEALED_OUT_DIR. Always sequenced after apply.
            ;; Carries the target's sealed_outputs so the runner stores
            ;; bytes via the configured provider.
            fetch-action (when (seq sealed-out)
                           (cond-> (-> {"id"             "fetch-secrets"
                                        "command"        ["bash" "-c"
                                                          (build-fetch-secrets-cmd bin sealed-out-outs)]
                                        "inputs"         {}
                                        "outputs"        []
                                        "depends_on"     ["apply"]
                                        "network"        true
                                        "impure"         true
                                        "work_dir"       dir
                                        "sealed_outputs" sealed-out}
                                       (attach-sealed-inputs sealed sealed-modes))
                             (seq sealed-out-modes) (assoc "sealed_output_modes" sealed-out-modes)))

            actions (cond-> [init-action plan-action]
                      auto?         (conj apply-action)
                      emit?         (conj show-action)
                      fetch-action  (conj fetch-action))

            declared (if emit?
                       {"terraform_state"   (str dir "/state.json")
                        "terraform_outputs" (str dir "/outputs.json")}
                       {})]

        {"actions"          actions
         "declared_outputs" declared}))))

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
