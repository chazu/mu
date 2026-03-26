#!/usr/bin/env bb

;; Lint plugin for mu.
;;
;; Wraps any linter as an observe/converge target. Language-agnostic —
;; the plugin runs a configurable command and interprets exit codes.
;;
;; Config options:
;;   command      - linter command as array (required)
;;                  e.g. ["golangci-lint", "run"]
;;   fix_command  - auto-fix command as array (optional)
;;                  e.g. ["golangci-lint", "run", "--fix"]
;;   env          - environment variables (optional)
;;   working_dir  - working directory for the command (optional)
;;
;; The plugin uses sources as the action's inputs for cache keying.
;; When sources change, the lint check re-runs.
;;
;; Observe behavior:
;;   exit 0 + no stdout = converged (lint passes)
;;   exit != 0 or stdout present = drifted (violations found)

(require '[cheshire.core :as json]
         '[clojure.string :as str]
         '[babashka.process :as process])

(defn handle-discover []
  {"name"             "lint"
   "version"          "0.1.0"
   "protocol_version" 1
   "capabilities"     ["discover" "plan" "observe"]
   "consumes"         ["source:any"]
   "produces"         []
   "config_schema"    {"command"     {"type" "array" "items" {"type" "string"}
                                      "required" true}
                       "fix_command" {"type" "array" "items" {"type" "string"}}
                       "env"         {"type" "object"}
                       "working_dir" {"type" "string"}}})

(defn handle-plan [req]
  (let [target  (get req "target")
        sources (get target "sources" [])
        config  (get target "config" {})
        command (get config "fix_command" (get config "command"))
        env     (get config "env" {})

        ;; Build input map from sources for cache keying.
        inputs (into {} (map (fn [s] [s s]) sources))]

    {"actions"
     [{"id"         "lint"
       "command"    command
       "inputs"     inputs
       "outputs"    []
       "depends_on" []
       "env"        env
       "network"    false
       "impure"     true}]
     "declared_outputs" {}}))

(defn handle-observe [req]
  (let [target  (get req "target")
        config  (get target "config" {})
        command (get config "command")
        env     (get config "env" {})
        workdir (get config "working_dir")]

    (when (nil? command)
      (throw (ex-info "lint plugin requires \"command\" in config" {})))

    (try
      (let [opts (cond-> {:out :string :err :string}
                   env     (assoc :extra-env env)
                   workdir (assoc :dir workdir))
            result (process/sh (into command) opts)]
        (cond
          ;; Exit 0 = lint passes = converged
          (= 0 (:exit result))
          (let [output (str/trim (str (:out result)))]
            (if (empty? output)
              {"state" "converged"}
              ;; Some linters exit 0 but print warnings — still converged
              ;; but include the output for visibility.
              {"state" "converged"}))

          ;; Non-zero exit = lint violations = drifted
          :else
          {"state" "drifted"
           "diff"  (let [out (str/trim (str (:out result)))
                         err (str/trim (str (:err result)))]
                     (str/join "\n" (remove empty? [out err])))}))

      (catch Exception e
        {"state" "drifted"
         "diff"  (str "lint command failed: " (.getMessage e))}))))

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
