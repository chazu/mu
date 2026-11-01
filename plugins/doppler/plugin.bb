#!/usr/bin/env bb

;; Read-only secret provider plugin backed by the Doppler CLI.
;;
;; Capabilities: discover, resolve_secret.
;;
;; Secret refs (resolve_secret):
;;   "doppler:<project>/<config>/<NAME>" -> the computed value of NAME in
;;                                         that config, byte for byte.
;;
;; mu strips the "doppler:" scheme, so this plugin sees
;; "<project>/<config>/<NAME>".
;;
;; One `doppler secrets download` per (project, config) per plugin process.
;; The result, or the failure, is kept in memory until mu closes stdin, so a
;; build that references one config 30 times calls the CLI once.
;;
;; Nothing here writes to disk or stderr, and no error message contains a
;; secret value: errors carry only names, refs and the CLI's own
;; "Doppler Error:" line.

(require '[cheshire.core :as json]
         '[babashka.process :as process]
         '[clojure.string :as str])

(def project-re #"[a-z0-9][a-z0-9_-]{0,63}")
(def config-re #"[a-z0-9][a-z0-9_-]{0,63}")
(def name-re #"[A-Za-z_][A-Za-z0-9_]{0,255}")

;; Doppler leaves a reference it can't resolve (renamed or deleted target,
;; no access) in the computed value as the literal "${...}".
(def unresolved-ref-re #"\$\{[A-Za-z0-9_.\-]+\}")

;; Stays under mu's 30s resolve_secret deadline: two attempts of at most 10s.
(def cli-deadline-ms 25000)

(defn handle-discover []
  {"name"             "doppler"
   "version"          "0.1.0"
   "protocol_version" 1
   "description"      "Resolve secrets from Doppler via the doppler CLI (read-only)"
   "consumes"         []
   "produces"         []
   "capabilities"     ["discover" "resolve_secret"]})

(defn parse-ref
  "Split '<project>/<config>/<NAME>' into [project config name], or nil when
   any part is malformed. The patterns also keep every part from starting
   with '-', so none can be read as a CLI flag."
  [ref]
  (when (string? ref)
    (let [parts (str/split ref #"/" -1)]
      (when (= 3 (count parts))
        (let [[p c n] parts]
          (when (and (re-matches project-re p)
                     (re-matches config-re c)
                     (re-matches name-re n))
            [p c n]))))))

(defn cli-error-line
  "The CLI's own error line, safe to show: only a 'Doppler Error:' line,
   truncated, with anything shaped like a Doppler token redacted."
  [err]
  (some-> (->> (str/split-lines (or err ""))
               ;; The CLI colours "Doppler Error:" even when stderr isn't a TTY.
               (map #(str/trim (str/replace % #"\u001B\[[0-9;]*[A-Za-z]" "")))
               (filter #(str/starts-with? % "Doppler Error:"))
               first)
          (str/replace #"dp\.[a-z]+(\.[A-Za-z0-9_\-]+)+" "dp.<redacted>")
          (as-> s (if (> (count s) 300) (str (subs s 0 300) "…") s))))

(defn download
  "Run the CLI for one config. Returns {:secrets {NAME value}} or {:error msg}.
   The CLI's stdout holds every value in the config, so no message built here
   includes any part of it."
  [project config]
  (let [argv ["doppler" "secrets" "download"
              "--no-file" "--format" "json"
              ;; No fallback file: nothing is written to or read from disk.
              "--no-fallback"
              ;; No update check: it writes a timestamp into the CLI config.
              "--no-check-version"
              ;; Ignore DOPPLER_* environment variables (token, API host,
              ;; TLS settings); the token comes from `doppler login`.
              "--no-read-env"
              "--silent"
              "--attempts" "2" "--timeout" "10s"
              "--project" project "--config" config]
        proc (try
               (process/process argv {:in "" :out :string :err :string})
               (catch Exception _ nil))]
    (if-not proc
      {:error "doppler: could not start the doppler CLI (is it on PATH?)"}
      (let [res (deref proc cli-deadline-ms ::timeout)]
        (if (= res ::timeout)
          (do (process/destroy-tree proc)
              {:error (str "doppler: " project "/" config ": the CLI timed out after "
                           (quot cli-deadline-ms 1000) "s")})
          (let [{:keys [exit out err]} res]
            (if-not (zero? exit)
              {:error (str "doppler: " project "/" config ": the CLI failed (exit " exit ")"
                           (when-let [line (cli-error-line err)] (str ": " line)))}
              (let [parsed (try (json/parse-string out) (catch Exception _ ::bad))]
                (if (and (map? parsed)
                         (every? string? (keys parsed))
                         (every? string? (vals parsed)))
                  {:secrets parsed}
                  {:error (str "doppler: " project "/" config
                               ": the CLI's output was not a JSON object of strings")})))))))))

(def downloads
  "[project config] -> download result, for the life of the process."
  (atom {}))

(defn fetch [project config]
  (let [k [project config]]
    (or (get @downloads k)
        (let [r (download project config)]
          (swap! downloads assoc k r)
          r))))

(defn handle-resolve-secret [req]
  (let [ref (get req "secret_ref")]
    (if-let [[p c n] (parse-ref ref)]
      (let [{:keys [secrets error]} (fetch p c)]
        (cond
          error
          {"value" "" "error" error}

          (not (contains? secrets n))
          {"value" "" "error" (str "doppler: " n " not found in " p "/" c)}

          (re-find unresolved-ref-re (get secrets n))
          {"value" "" "error" (str "doppler: " p "/" c "/" n
                                   " contains an unresolved ${...} reference;"
                                   " fix the reference in Doppler")}

          :else
          {"value" (get secrets n)}))
      {"value" ""
       "error" (let [shown (pr-str ref)]
                 (str "doppler: invalid ref "
                      (if (> (count shown) 100) (str (subs shown 0 100) "…") shown)
                      "; expected doppler:<project>/<config>/<NAME>"))})))

(defn handle-request [req]
  (case (get req "method")
    "discover"       (handle-discover)
    "resolve_secret" (handle-resolve-secret req)
    "store_secret"   {"error" "doppler: store_secret is not supported; the plugin is read-only"}
    {"error" (str "doppler: unknown method " (pr-str (get req "method")))}))

(defn respond [line]
  (try
    (let [req (try (json/parse-string line) (catch Exception _ ::bad))]
      (if (map? req)
        (handle-request req)
        {"error" "doppler: request was not a JSON object"}))
    (catch Exception e
      ;; Never the exception's message: it could quote CLI output.
      {"value" "" "error" (str "doppler: internal error (" (.getName (class e)) ")")})))

(loop []
  (when-let [line (read-line)]
    (println (json/generate-string (respond line)))
    (flush)
    (recur)))
