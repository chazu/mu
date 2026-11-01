#!/usr/bin/env bb

(require '[cheshire.core :as json])

(defn handle-discover []
  {"name"             "void"
   "version"          "0.1.0"
   "protocol_version" 1
   "description"      "Post build lifecycle events to a webhook endpoint"
   "consumes"         []
   "produces"         []
   "capabilities"     ["discover" "advise"]
   "advise_phases"    ["after-build"]})

(defn compute-hmac [payload secret]
  (let [mac (javax.crypto.Mac/getInstance "HmacSHA256")
        key (javax.crypto.spec.SecretKeySpec.
              (.getBytes secret "UTF-8") "HmacSHA256")]
    (.init mac key)
    (let [raw (.doFinal mac (.getBytes payload "UTF-8"))]
      (apply str (map #(format "%02x" %) raw)))))

(defn handle-advise [req]
  (let [config  (get req "advise_config" {})
        secrets (get req "secrets" {})
        url     (get config "webhook_url")
        secret  (get secrets "hmac_secret")]
    (if (nil? url)
      {"ok" false "error" "no webhook_url in advice config"}
      (try
        (let [payload (json/generate-string
                        (merge (get req "manifest" {})
                               {"mu_context" (get req "advise_context" {})}))
              headers (cond-> {"Content-Type" "application/json"}
                        secret
                        (assoc "X-Hub-Signature-256"
                               (str "sha256=" (compute-hmac payload secret))))]
          (binding [*err* (java.io.PrintWriter. (java.io.StringWriter.))]
            (let [resp (babashka.http-client/post url
                         {:headers headers
                          :body    payload
                          :timeout 10000})]
              (if (<= 200 (:status resp) 299)
                {"ok" true}
                {"ok"    false
                 "error" (str "webhook returned HTTP " (:status resp))}))))
        (catch Exception e
          {"ok"    false
           "error" (str "webhook POST failed: " (.getMessage e))})))))

(defn handle-request [req]
  (case (get req "method")
    "discover" (handle-discover)
    "advise"   (handle-advise req)
    {"error" (str "unknown method: " (get req "method"))}))

(loop []
  (when-let [line (read-line)]
    (let [req  (json/parse-string line)
          resp (handle-request req)]
      (println (json/generate-string resp))
      (flush)
      (recur))))
