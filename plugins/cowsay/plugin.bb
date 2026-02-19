#!/usr/bin/env bb

(require '[cheshire.core :as json])

(defn handle-discover []
  {"name"             "cowsay"
   "version"          "0.1.0"
   "protocol_version" 1
   "consumes"         ["source:text"]
   "produces"         ["text_output"]})

(defn handle-plan [req]
  (let [target  (get req "target")
        sources (get target "sources")
        config  (get target "config")
        input   (first sources)
        output  (get config "output" "output.txt")]
    {"actions"
     [{"id"         "cowsay"
       "command"    ["sh" "-c" (str "cowsay < " input " > " output)]
       "inputs"     {"source" input}
       "outputs"    [output]
       "depends_on" []
       "env"        {}
       "network"    false}]
     "declared_outputs"
     {"text_output" output}}))

(defn handle-request [req]
  (case (get req "method")
    "discover" (handle-discover)
    "plan"     (handle-plan req)))

(loop []
  (when-let [line (read-line)]
    (let [req  (json/parse-string line)
          resp (handle-request req)]
      (println (json/generate-string resp))
      (flush)
      (recur))))
