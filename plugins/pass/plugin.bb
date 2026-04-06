#!/usr/bin/env bb

;; Secret provider plugin backed by pass (https://passwordstore.org).
;;
;; Capabilities: discover, resolve_secret.
;;
;; Secret references use the format "pass:<path>" in sealed_inputs.
;; The plugin runs `pass show <path>` and returns the first line
;; (the secret value, not metadata lines).

(require '[cheshire.core :as json]
         '[babashka.process :as process]
         '[clojure.string :as str])

(defn handle-discover []
  {"name"             "pass"
   "version"          "0.1.0"
   "protocol_version" 1
   "consumes"         []
   "produces"         []
   "capabilities"     ["discover" "resolve_secret"]})

(defn handle-resolve-secret [req]
  (let [path   (get req "secret_ref")
        result (process/sh ["pass" "show" path])]
    (if (zero? (:exit result))
      (let [value (first (str/split-lines (:out result)))]
        {"value" (str/trim (or value ""))})
      {"error" (str "pass show failed (exit " (:exit result) "): "
                    (str/trim (:err result)))})))

(defn handle-request [req]
  (case (get req "method")
    "discover"       (handle-discover)
    "resolve_secret" (handle-resolve-secret req)
    {"error" (str "unknown method: " (get req "method"))}))

;; Main NDJSON loop
(loop []
  (when-let [line (read-line)]
    (let [req  (json/parse-string line)
          resp (handle-request req)]
      (println (json/generate-string resp))
      (flush)
      (recur))))
