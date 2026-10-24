#!/usr/bin/env bb
;; Tests for plugins/doppler/plugin.bb. Run: bb plugins/doppler/test/plugin_test.bb
;;
;; Each test starts the plugin as mu would (NDJSON over stdin/stdout) with the
;; fake `doppler` from this directory first on PATH. Every secret value the
;; fake serves contains a canary; the fixture fails any test in which the
;; canary reaches an error message, a non-value response field or the
;; plugin's stderr.

(require '[babashka.fs :as fs]
         '[babashka.process :as process]
         '[cheshire.core :as json]
         '[clojure.java.io :as io]
         '[clojure.string :as str]
         '[clojure.test :refer [deftest is testing run-tests]])

(def here (fs/parent (fs/absolutize *file*)))
(def plugin (str (fs/path here ".." "plugin.bb")))
(def canary (str "CANARY-" (random-uuid)))

(def ^:dynamic *dir* nil)

(defn serve!
  "Configure the fake CLI's answer for project/config."
  [project config & {:keys [out err exit hang]}]
  (let [base (str project "__" config)]
    (when out (spit (io/file *dir* (str base ".out")) out))
    (when err (spit (io/file *dir* (str base ".err")) err))
    (when exit (spit (io/file *dir* (str base ".exit")) (str exit)))
    (when hang (spit (io/file *dir* (str base ".hang")) ""))))

(defn calls []
  (let [f (io/file *dir* "calls.jsonl")]
    (if (.exists f) (mapv json/parse-string (line-seq (io/reader f))) [])))

(defn start-plugin [& {:keys [path]}]
  (let [p (process/process ["bb" plugin]
                           {:dir (str *dir*)
                            :extra-env {"FAKE_DOPPLER_DIR" (str *dir*)
                                        "PATH" (or path (str here ":" (System/getenv "PATH")))}
                            :err :string})]
    {:proc p
     :in   (io/writer (:in p))
     :out  (io/reader (:out p))}))

(defn send! [{:keys [in out]} req]
  (.write in (str (if (string? req) req (json/generate-string req)) "\n"))
  (.flush in)
  (json/parse-string (.readLine out)))

(defn stop-plugin
  "Close stdin like mu does, wait for exit and return the plugin's stderr."
  [{:keys [proc in]}]
  (.close in)
  (let [{:keys [exit err]} @proc]
    (is (zero? exit) "plugin exits cleanly on EOF")
    err))

(defn leaks?
  "True when the canary appears anywhere in a response except its value."
  [resp]
  (str/includes? (json/generate-string (dissoc resp "value")) canary))

(defmacro with-plugin [[sym & opts] & body]
  `(let [dir# (str (fs/create-temp-dir {:prefix "mu-doppler-test-"}))]
     (binding [*dir* dir#]
       (try
         (let [~sym (start-plugin ~@opts)
               res# (do ~@body)
               err# (stop-plugin ~sym)]
           (is (= "" err#) "plugin writes nothing to stderr")
           res#)
         (finally (fs/delete-tree dir#))))))

(defn resolve! [plugin ref]
  (let [resp (send! plugin {"method" "resolve_secret" "secret_ref" ref})]
    (is (not (leaks? resp)) (str "canary leaked for ref " (pr-str ref)))
    (when (get resp "error") (is (= "" (get resp "value")) "an error carries an empty value"))
    resp))

(def values
  {"PLAIN"     (str canary "-plain")
   "MULTILINE" (str "-----BEGIN KEY-----\n" canary "\n-----END KEY-----\n")
   "UNICODE"   (str "pässwörd-🔑-" canary)
   "QUOTES"    (str "a\"b'c\\d\t" canary)
   "EMPTY"     ""
   "DOLLAR"    (str "a$b${c" canary)})

;;; ─── discover and protocol ─────────────────────────────────────────────

(deftest discover-is-read-only
  (with-plugin [p]
    (let [d (send! p {"method" "discover"})]
      (is (= "doppler" (get d "name")))
      (is (= 1 (get d "protocol_version")))
      (is (= ["discover" "resolve_secret"] (get d "capabilities"))))))

(deftest store-secret-is-refused-without-calling-the-cli
  (with-plugin [p]
    (let [r (send! p {"method" "store_secret" "secret_ref" "hosts/prd/X"
                      "secret_value" canary "secret_mode" "overwrite"})]
      (is (str/includes? (get r "error") "read-only"))
      (is (not (leaks? r)))
      (is (empty? (calls))))))

(deftest bad-requests-get-errors-and-the-plugin-keeps-serving
  (with-plugin [p]
    (serve! "hosts" "prd" :out (json/generate-string values))
    (doseq [line ["not json" "[]" "42" "null" "{\"method\": \"nope\"}" "{}"]]
      (let [r (send! p line)]
        (is (string? (get r "error")) (str "error for " line))))
    (is (= (values "PLAIN") (get (resolve! p "hosts/prd/PLAIN") "value"))
        "still serving after bad requests")))

;;; ─── resolving ─────────────────────────────────────────────────────────

(deftest values-come-back-byte-for-byte
  (with-plugin [p]
    (serve! "hosts" "prd_dalian" :out (json/generate-string values))
    (doseq [[n v] values]
      (let [r (resolve! p (str "hosts/prd_dalian/" n))]
        (is (nil? (get r "error")) n)
        (is (= v (get r "value")) n)))))

(deftest cli-gets-fixed-flags-empty-stdin-and-no-token
  (with-plugin [p]
    (serve! "hosts" "prd_void" :out (json/generate-string values))
    (resolve! p "hosts/prd_void/PLAIN")
    (let [[c & more] (calls)]
      (is (empty? more))
      (is (= ["secrets" "download" "--no-file" "--format" "json" "--no-fallback"
              "--no-check-version" "--no-read-env" "--silent" "--attempts" "2"
              "--timeout" "10s" "--project" "hosts" "--config" "prd_void"]
             (get c "argv")))
      (is (= "" (get c "stdin"))))))

(deftest one-cli-call-per-config-including-failures
  (with-plugin [p]
    (serve! "hosts" "prd_dalian" :out (json/generate-string values))
    (serve! "hosts" "prd_kiruko" :out (json/generate-string values))
    (serve! "hosts" "broken" :exit 1 :err "Doppler Error: Could not find requested config 'broken'\n")
    (doseq [n ["PLAIN" "UNICODE" "MULTILINE"]]
      (resolve! p (str "hosts/prd_dalian/" n))
      (resolve! p (str "hosts/prd_kiruko/" n)))
    (dotimes [_ 3] (resolve! p "hosts/broken/PLAIN"))
    (is (= [["hosts" "prd_dalian"] ["hosts" "prd_kiruko"] ["hosts" "broken"]]
           (map #(let [a (get % "argv")] [(nth a (- (count a) 3)) (last a)]) (calls))))))

(deftest invalid-refs-never-reach-the-cli
  (with-plugin [p]
    (doseq [ref ["" "hosts" "hosts/prd" "hosts/prd/A/B" "/prd/A" "hosts//A" "hosts/prd/"
                 "-p/prd/A" "hosts/-c/A" "hosts/prd/-A" "--token=x/prd/A" "hosts/--config=x/A"
                 "Hosts/prd/A" "hosts/PRD/A" "hosts/prd/A\n" "hosts/prd/A B" "../prd/A"
                 "hosts/../A" "hosts/prd/1A" "hosts/prd/A;id" "hosts/prd/$(id)" "hosts/prd/ÑAME"
                 "hosts\u0000/prd/A" "hosts/prd/A-B" nil 42 ["hosts/prd/A"]]]
      (let [r (resolve! p ref)]
        (is (str/includes? (str (get r "error")) "invalid ref") (pr-str ref))))
    (is (empty? (calls)))))

(deftest missing-name-is-an-error-that-lists-nothing-else
  (with-plugin [p]
    (serve! "hosts" "prd" :out (json/generate-string values))
    (let [r (resolve! p "hosts/prd/NOPE")]
      (is (= "doppler: NOPE not found in hosts/prd" (get r "error"))))))

(deftest unresolved-references-are-errors-without-the-value
  (with-plugin [p]
    (serve! "hosts" "prd" :out (json/generate-string
                                {"WHOLE"  "${database.prd.SNOOKER_PASSWORD}"
                                 "INSIDE" (str canary "-${OTHER}-" canary)
                                 "LOCAL"  "${SSH_PASS}"}))
    (doseq [n ["WHOLE" "INSIDE" "LOCAL"]]
      (let [r (resolve! p (str "hosts/prd/" n))]
        (is (str/includes? (str (get r "error")) "unresolved") n)
        (is (not (str/includes? (get r "error") "SNOOKER")) "the reference text isn't echoed")))))

;;; ─── CLI failures ──────────────────────────────────────────────────────

(deftest cli-failure-shows-only-the-redacted-doppler-error-line
  (with-plugin [p]
    (serve! "hosts" "prd" :exit 1
            :out (json/generate-string values)
            :err (str "some debug line " canary "\n"
                      "Doppler Error: Invalid Auth token dp.st.prd.AbC123_xyz-9 for " "hosts\n"
                      "trailing " canary "\n"))
    (let [r (resolve! p "hosts/prd/PLAIN")
          e (get r "error")]
      (is (str/includes? e "exit 1"))
      (is (str/includes? e "Doppler Error: Invalid Auth token dp.<redacted>"))
      (is (not (str/includes? e "AbC123"))))))

(deftest cli-failure-with-the-real-clis-coloured-error
  (with-plugin [p]
    (serve! "hosts" "nope" :exit 1
            :err "Unable to download secrets\n\u001B[31mDoppler Error:\u001B[0m Could not find requested config 'nope'\n")
    (is (= "doppler: hosts/nope: the CLI failed (exit 1): Doppler Error: Could not find requested config 'nope'"
           (get (resolve! p "hosts/nope/PLAIN") "error")))))

(deftest cli-failure-without-an-error-line
  (with-plugin [p]
    (serve! "hosts" "prd" :exit 2 :err (str canary "\n"))
    (is (= "doppler: hosts/prd: the CLI failed (exit 2)"
           (get (resolve! p "hosts/prd/PLAIN") "error")))))

(deftest unparseable-output-is-not-quoted
  (doseq [out [(str "{\"PLAIN\": \"" canary)               ; truncated JSON
               (str "PLAIN=" canary "\n")                    ; env format
               (json/generate-string [canary])               ; array
               (json/generate-string {"PLAIN" {"computed" canary}}) ; nested
               (json/generate-string {"PLAIN" 42 "X" canary})       ; non-string value
               ""]]
    (with-plugin [p]
      (serve! "hosts" "prd" :out out)
      (let [r (resolve! p "hosts/prd/PLAIN")]
        (is (str/includes? (str (get r "error")) "not a JSON object of strings") (pr-str out))))))

(deftest missing-cli
  (with-plugin [p :path "/nonexistent"]
    (is (str/includes? (get (resolve! p "hosts/prd/PLAIN") "error") "could not start"))))

(deftest hung-cli-is-killed-before-mus-deadline
  (with-plugin [p]
    (serve! "hosts" "prd" :hang true)
    (let [t0 (System/currentTimeMillis)
          r  (resolve! p "hosts/prd/PLAIN")
          dt (- (System/currentTimeMillis) t0)
          pid (parse-long (slurp (io/file *dir* "pid-0")))]
      (is (str/includes? (get r "error") "timed out"))
      (is (< dt 30000) "answers inside mu's 30s resolve_secret timeout")
      (Thread/sleep 200)
      (is (not (.isPresent (java.lang.ProcessHandle/of pid))) "the CLI process is gone"))))

(let [{:keys [fail error]} (run-tests)]
  (System/exit (if (zero? (+ fail error)) 0 1)))
