#!/usr/bin/env bb

;; sops: bidirectional secret provider backed by Mozilla SOPS.
;;
;; Capabilities: discover, resolve_secret, store_secret.
;;
;; Ref grammar:
;;   sops:<file>#<dotted.key.path>
;;
;; Examples:
;;   sops:secrets/prod.yaml#database.password
;;   sops:env/dev.json#aws.access_key_id
;;
;; The plugin shells out to the `sops` CLI for both read and write. The
;; mu runner strips the "sops:" scheme before sending requests, so this
;; plugin sees secret_ref values like "secrets/prod.yaml#database.password".
;;
;; PRECONDITIONS
;;   - `sops` 3.7+ must be on PATH (we use the `set` subcommand).
;;   - The encrypted file (or a project-level .sops.yaml with creation
;;     rules covering the file path) must already exist. v0.1 does not
;;     auto-bootstrap empty encrypted files.
;;   - Whatever credentials sops needs (gpg agent, AWS KMS, age keys)
;;     must already be available to the running user. Forward keys via
;;     env vars in the action that triggered this plugin if needed.
;;
;; STORE MODES
;;   create            Fail if the value already exists at this path.
;;   overwrite         Always set (creating intermediate keys).
;;   create_if_absent  No-op if value exists; create otherwise.

(require '[cheshire.core :as json]
         '[clojure.string :as str]
         '[babashka.process :as process])

(defn handle-discover []
  {"name"             "sops"
   "version"          "0.1.0"
   "protocol_version" 1
   "description"      "Resolve and store secrets in SOPS-encrypted files"
   "consumes"         []
   "produces"         []
   "capabilities"     ["discover" "resolve_secret" "store_secret"]})

;;; ─── Ref parsing ──────────────────────────────────────────────────────

(defn parse-ref
  "Parse '<file>#<dotted.key.path>' into [file key-path]. Throws on
   malformed input. The dotted path must have at least one segment."
  [ref]
  (let [hash-idx (str/index-of ref "#")]
    (when-not hash-idx
      (throw (ex-info (str "sops: ref must be of form '<file>#<dotted.key>' (got " (pr-str ref) ")")
                      {:ref ref})))
    (let [file (subs ref 0 hash-idx)
          key  (subs ref (inc hash-idx))]
      (when (str/blank? file)
        (throw (ex-info "sops: ref file part is empty" {:ref ref})))
      (when (str/blank? key)
        (throw (ex-info "sops: ref key path is empty" {:ref ref})))
      [file key])))

(defn key-path->sops-extract
  "Convert a dotted key path 'a.b.c' into sops --extract syntax
   '[\"a\"][\"b\"][\"c\"]'. We do not currently parse '[N]' for array
   indexing; if a user needs that, document it as a limitation."
  [dotted]
  (->> (str/split dotted #"\.")
       (map #(str "[\"" % "\"]"))
       (apply str)))

;;; ─── Helpers ──────────────────────────────────────────────────────────

(defn run-sh
  "Run argv with optional stdin string. Returns {:exit :out :err}."
  ([argv] (run-sh argv nil))
  ([argv stdin-str]
   (let [opts (cond-> {:out :string :err :string}
                stdin-str (assoc :in stdin-str))
         {:keys [exit out err]} @(process/process argv opts)]
     {:exit exit :out (str out) :err (str err)})))

(defn extract-value
  "Decrypt the value at key-path from file. Returns {:found bool, :value
   string or nil, :error string or nil}. A nonzero sops exit code with
   a 'failed to navigate' / 'no such key' style message means not-found,
   not an error."
  [file dotted-key]
  (let [extract (key-path->sops-extract dotted-key)
        result  (run-sh ["sops" "--decrypt" "--extract" extract file])]
    (cond
      (zero? (:exit result))
      ;; sops appends a trailing newline to extracted scalar values; strip
      ;; it so callers get the raw bytes the user wrote in.
      {:found true :value (str/replace (:out result) #"\n\z" "")}

      ;; Heuristic: sops uses these phrases for missing-key cases. If we
      ;; can't tell, treat as error.
      (re-find #"(?i)does not exist|cannot fetch|no such key|key not found|empty key"
               (:err result))
      {:found false}

      :else
      {:found false :error (str "sops --extract failed (exit " (:exit result) "): "
                                (str/trim (:err result)))})))

;;; ─── resolve_secret ───────────────────────────────────────────────────

(defn handle-resolve-secret [req]
  (let [ref (get req "secret_ref")]
    (try
      (let [[file dotted-key] (parse-ref ref)
            r                 (extract-value file dotted-key)]
        (cond
          (:error r)        {"value" "" "error" (:error r)}
          (not (:found r))  {"value" "" "error" (str "sops: ref " ref " not found")}
          :else             {"value" (:value r)}))
      (catch Exception e
        {"value" "" "error" (.getMessage e)}))))

;;; ─── store_secret ─────────────────────────────────────────────────────

(defn sops-set
  "Run `sops set FILE KEY-PATH JSON-VALUE`. Returns {:exit :err}."
  [file dotted-key value]
  (let [extract (key-path->sops-extract dotted-key)
        ;; sops set expects a JSON-encoded value as the third argument.
        ;; A string becomes \"hello\"; a number 42; a bool true/false.
        json-val (json/generate-string value)
        result   (run-sh ["sops" "set" file extract json-val])]
    {:exit (:exit result) :err (:err result)}))

(defn handle-store-secret [req]
  (let [ref   (get req "secret_ref")
        value (get req "secret_value")
        mode  (or (get req "secret_mode") "overwrite")]
    (cond
      (nil? value)
      {"error" "store_secret requires secret_value"}

      (not (#{"create" "overwrite" "create_if_absent"} mode))
      {"error" (str "sops: unknown secret_mode " (pr-str mode))}

      :else
      (try
        (let [[file dotted-key] (parse-ref ref)
              probe             (extract-value file dotted-key)
              exists?           (:found probe)]
          (cond
            (and (= mode "create") exists?)
            {"error" (str "sops: ref " ref " already exists (mode=create)")}

            (and (= mode "create_if_absent") exists?)
            {}

            :else
            (let [r (sops-set file dotted-key value)]
              (if (zero? (:exit r))
                {}
                {"error" (str "sops set failed (exit " (:exit r) "): "
                              (str/trim (:err r)))}))))
        (catch Exception e
          {"error" (.getMessage e)})))))

;;; ─── Dispatch ─────────────────────────────────────────────────────────

(defn handle-request [req]
  (case (get req "method")
    "discover"       (handle-discover)
    "resolve_secret" (handle-resolve-secret req)
    "store_secret"   (handle-store-secret req)
    {"error" (str "unknown method: " (get req "method"))}))

(loop []
  (when-let [line (read-line)]
    (println (json/generate-string (handle-request (json/parse-string line))))
    (flush)
    (recur)))
