#!/usr/bin/env bb

;; Lint plugin for mu.
;;
;; Wraps any linter as a build target. Language-agnostic — the plugin
;; runs a configurable command and fails the build on a non-zero exit.
;;
;; Config options:
;;   command      - linter command as array (required)
;;                  e.g. ["golangci-lint", "run"]
;;   fix_command  - auto-fix command as array (optional)
;;                  e.g. ["golangci-lint", "run", "--fix"]
;;   fix          - when true, plan fix_command instead of command
;;                  (optional, default false). Without it, `mu build`
;;                  only checks and never rewrites sources.
;;   env          - complete environment for the command (optional; when
;;                  unset, the impure action inherits mu's environment)
;;   working_dir  - working directory for the command (optional)
;;
;; Sources are declared as the action's inputs. The action is impure
;; (it runs in the project tree and is never cached), so a lint target
;; re-runs on every build; a non-zero exit fails the build.

(require '[cheshire.core :as json])

(defn handle-discover []
  {"name"             "lint"
   "version"          "0.2.0"
   "protocol_version" 1
   "description"      "Run linter or code-quality commands against source files"
   "capabilities"     ["discover" "plan"]
   "consumes"         ["source:any"]
   "produces"         []
   "config_schema"    {"command"     {"type" "array" "items" {"type" "string"}
                                      "required" true "description" "Lint command to execute"}
                       "fix_command" {"type" "array" "items" {"type" "string"}
                                      "description" "Auto-fix command (planned instead of command only when fix is true)"}
                       "fix"         {"type" "boolean"
                                      "description" "Plan fix_command instead of command (rewrites sources)"}
                       "env"         {"type" "object" "description" "Complete environment for the lint process (default: inherit mu's)"}
                       "working_dir" {"type" "string" "description" "Working directory for the lint command"}}})

(defn handle-plan [req]
  (let [target  (get req "target")
        sources (get target "sources" [])
        config  (get target "config" {})
        fix?    (true? (get config "fix"))
        command (if fix?
                  (get config "fix_command")
                  (get config "command"))
        env     (get config "env")
        workdir (get config "working_dir")

        inputs (into {} (map (fn [s] [s s]) sources))]

    (if (nil? command)
      {"error" (if fix?
                 "lint plugin: fix is true but \"fix_command\" is not set"
                 "lint plugin requires \"command\" in config")}
      {"actions"
       ;; "env" is emitted only when configured: an impure action without
       ;; env inherits mu's environment (PATH, HOME, GOCACHE, ...), while
       ;; an explicit env map replaces it entirely.
       [(cond-> {"id"         "lint"
                 "command"    command
                 "inputs"     inputs
                 "outputs"    []
                 "depends_on" []
                 "network"    false
                 "impure"     true}
          (seq env) (assoc "env" env)
          (seq workdir) (assoc "work_dir" workdir))]
       "declared_outputs" {}})))

(defn handle-request [req]
  (case (get req "method")
    "discover" (handle-discover)
    "plan"     (handle-plan req)
    {"error" (str "unknown method: " (get req "method"))}))

;; Main NDJSON loop
(loop []
  (when-let [line (read-line)]
    (let [req  (json/parse-string line)
          resp (handle-request req)]
      (println (json/generate-string resp))
      (flush)
      (recur))))
