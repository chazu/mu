#!/usr/bin/env bb

;; Secret provider plugin backed by pass (https://passwordstore.org).
;;
;; Capabilities: discover, resolve_secret, store_secret.
;;
;; Secret refs (resolve_secret):
;;   "pass:<path>"     -> first line of `pass show <path>` (typical password).
;;   "pass:raw:<path>" -> full output of `pass show <path>` with trailing
;;                        newlines trimmed. For multi-line secrets such as
;;                        SSH private keys, certificates, or JSON blobs.
;;
;; Secret refs (store_secret): always raw bytes; the "raw:" prefix is
;; ignored on write because there is no first-line/full-content
;; distinction at insert time. Modes: "create" (fail if exists),
;; "overwrite" (always set), "create_if_absent" (no-op if exists).

(require '[cheshire.core :as json]
         '[babashka.process :as process]
         '[clojure.string :as str])

(defn handle-discover []
  {"name"             "pass"
   "version"          "0.3.0"
   "protocol_version" 1
   "description"      "Resolve and store secrets via the pass password manager"
   "consumes"         []
   "produces"         []
   "capabilities"     ["discover" "resolve_secret" "store_secret"]})

(defn handle-resolve-secret [req]
  (let [ref    (get req "secret_ref")
        raw?   (str/starts-with? ref "raw:")
        path   (if raw? (subs ref 4) ref)
        result (process/sh ["pass" "show" path])]
    (if (zero? (:exit result))
      (let [out (:out result)]
        (if raw?
          {"value" (str/replace out #"\n+\z" "")}
          {"value" (str/trim (or (first (str/split-lines out)) ""))}))
      {"error" (str "pass show failed (exit " (:exit result) "): "
                    (str/trim (:err result)))})))

(defn pass-exists? [path]
  (zero? (:exit (process/sh ["pass" "show" path]))))

(defn pass-insert [path value]
  ;; `pass insert -m -f` reads multi-line input from stdin until EOF and
  ;; overwrites any existing entry. We always feed raw bytes; the caller
  ;; is responsible for whatever encoding they want stored.
  (process/shell {:in value} "pass" "insert" "-m" "-f" path))

(defn handle-store-secret [req]
  (let [ref   (get req "secret_ref")
        ;; Strip a leading "raw:" if present: it's a read-time hint and
        ;; has no meaning at insert time. The path under pass is identical.
        path  (if (str/starts-with? ref "raw:") (subs ref 4) ref)
        value (get req "secret_value")
        mode  (get req "secret_mode")]
    (cond
      (nil? value)
      {"error" "store_secret requires secret_value"}

      (or (= mode "create") (= mode "create_if_absent"))
      (if (pass-exists? path)
        (if (= mode "create_if_absent")
          {}
          {"error" (str "pass entry already exists: " path)})
        (let [r (pass-insert path value)]
          (if (zero? (:exit r))
            {}
            {"error" (str "pass insert failed (exit " (:exit r) "): "
                          (str/trim (or (:err r) "")))})))

      (or (= mode "overwrite") (nil? mode) (= mode ""))
      (let [r (pass-insert path value)]
        (if (zero? (:exit r))
          {}
          {"error" (str "pass insert failed (exit " (:exit r) "): "
                        (str/trim (or (:err r) "")))}))

      :else
      {"error" (str "unknown secret_mode: " mode)})))

(defn handle-request [req]
  (case (get req "method")
    "discover"       (handle-discover)
    "resolve_secret" (handle-resolve-secret req)
    "store_secret"   (handle-store-secret req)
    {"error" (str "unknown method: " (get req "method"))}))

(loop []
  (when-let [line (read-line)]
    (let [req  (json/parse-string line)
          resp (handle-request req)]
      (println (json/generate-string resp))
      (flush)
      (recur))))
