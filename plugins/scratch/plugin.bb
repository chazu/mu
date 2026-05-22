#!/usr/bin/env bb

(require '[cheshire.core :as json])

(defn discover-response []
  {"name"             "scratch"
   "version"          "0.1.0"
   "protocol_version" 1
   "description"      "Download, verify, and extract toolchain archives"
   "consumes"         []
   "produces"         ["toolchain"]})

(defn plan-response [req]
  (let [target  (get req "target")
        config  (get target "config")
        url     (get config "url")
        sha256  (get config "sha256")
        version (get config "version")
        strip   (get config "strip_prefix")
        tgt-name (get target "name" "tool")
        ;; Derive a short name from the target for file paths
        short-name (last (clojure.string/split tgt-name #"[:/]"))
        out-dir    (str "toolchains/" short-name "/" version)
        archive    (str out-dir "/download/" (last (clojure.string/split url #"/")))
        bin-dir    (str out-dir "/bin")
        manifest   (str out-dir "/manifest.json")]
    {"actions"
     [;; 1. fetch — download and verify checksum
      {"id"       (str short-name "/fetch")
       "command"  ["sh" "-c"
                   (str "mkdir -p $(dirname '" archive "') && "
                        "curl -fsSL '" url "' -o '" archive "' && "
                        "echo '" sha256 "  " archive "' | "
                        ;; portable: try sha256sum, fall back to shasum -a 256
                        "if command -v sha256sum >/dev/null 2>&1; then sha256sum -c; else shasum -a 256 -c; fi")]
       "network"  true
       "depends_on"  []
       "outputs"  [archive]}

      ;; 2. extract — unpack or copy into bin dir
      {"id"       (str short-name "/extract")
       "command"  ["sh" "-c"
                   (let [is-tar   (or (clojure.string/ends-with? url ".tar.gz")
                                      (clojure.string/ends-with? url ".tgz"))
                         is-zip   (clojure.string/ends-with? url ".zip")
                         strip-flag (when strip (str "--strip-components=1 "))]
                     (str "mkdir -p '" bin-dir "' && "
                          (cond
                            is-tar  (str "tar xzf '" archive "' -C '" bin-dir "' " (or strip-flag ""))
                            is-zip  (str "unzip -o '" archive "' -d '" bin-dir "'")
                            ;; Single binary — just copy and make executable
                            :else   (str "cp '" archive "' '" bin-dir "/" short-name "' && "
                                         "chmod +x '" bin-dir "/" short-name "'"))))]
       "depends_on"  [(str short-name "/fetch")]
       "outputs"  [(str bin-dir "/" short-name)]}

      ;; 3. verify — run the binary with --version
      {"id"       (str short-name "/verify")
       "command"  ["sh" "-c"
                   (str "'" bin-dir "/" short-name "' --version")]
       "depends_on"  [(str short-name "/extract")]
       "outputs"  []}

      ;; 4. register — write a manifest JSON
      {"id"       (str short-name "/register")
       "command"  ["sh" "-c"
                   (str "cat > '" manifest "' <<'MANIFEST_EOF'\n"
                        (json/generate-string
                         {"name"    short-name
                          "version" version
                          "bin_dir" bin-dir
                          "sha256"  sha256}
                         {:pretty true})
                        "\nMANIFEST_EOF")]
       "depends_on"  [(str short-name "/verify")]
       "outputs"  [manifest]}]

     "declared_outputs" {"toolchain_bin" bin-dir
                         "manifest"      manifest}}))

(defn handle-request [req]
  (case (get req "method")
    "discover" (discover-response)
    "plan"     (plan-response req)
    {"error" (str "unknown method: " (get req "method"))}))

;; Main NDJSON loop
(loop []
  (when-let [line (read-line)]
    (let [req  (json/parse-string line)
          resp (handle-request req)]
      (println (json/generate-string resp))
      (flush)
      (recur))))
