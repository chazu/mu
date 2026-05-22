#!/usr/bin/env bb

;; keypair-gen: generate an EC keypair (ed25519 or ecdsa) and route both
;; halves into sealed_outputs so they land in a secret backend instead of
;; on disk or in CAS. Implements discover and plan.
;;
;; Config:
;;   type    (string, default "ed25519")  — "ed25519" or "ecdsa"
;;   curve   (string, default "P-256")    — only for type:"ecdsa";
;;                                          one of "P-256", "P-384", "P-521"
;;   comment (string, optional)            — ssh-keygen -C value (defaults
;;                                          to the target name)
;;
;; Sealed outputs CONTRACT (target side):
;;   sealed_outputs MUST contain exactly two keys named "PRIVATE" and
;;   "PUBLIC". The plan rejects any other key set.
;;
;;   sealed_outputs:      {PRIVATE: "pass:raw:deploy/key", PUBLIC: "pass:deploy/key.pub"}
;;   sealed_output_modes: {PRIVATE: "create_if_absent",     PUBLIC: "create_if_absent"}
;;
;; The action runs ssh-keygen (no passphrase) into a per-action tmp dir,
;; moves both files into $MU_SEALED_OUT_DIR, chmods 0600, and cleans up.
;; The runner then routes each through the configured provider's
;; store_secret. Pair PRIVATE with the "raw:" prefix on read so the full
;; multi-line key is preserved.

(require '[cheshire.core :as json]
         '[clojure.string :as str])

(defn handle-discover []
  {"name"             "keypair-gen"
   "version"          "0.1.0"
   "protocol_version" 1
   "description"      "Generate SSH key pairs (Ed25519 or ECDSA) as sealed outputs"
   "consumes"         []
   "produces"         []
   "capabilities"     ["discover" "plan"]
   "config_schema"    {"type"    {"type" "string" "default" "ed25519"
                                  "enum" ["ed25519" "ecdsa"] "description" "Key algorithm"}
                       "curve"   {"type" "string" "default" "P-256"
                                  "enum" ["P-256" "P-384" "P-521"] "description" "ECDSA curve (ignored for Ed25519)"}
                       "comment" {"type" "string" "description" "Key comment embedded in the public key"}}})

(defn shell-quote
  "Single-quote a string for safe inclusion in a bash command."
  [s]
  (str "'" (str/replace (str s) "'" "'\\''") "'"))

(def required-output-names #{"PRIVATE" "PUBLIC"})

(def curve-bits
  "Map ECDSA curve names to ssh-keygen -b values."
  {"P-256" "256"
   "P-384" "384"
   "P-521" "521"})

(defn keygen-args
  "Build the ssh-keygen flags for the requested key type."
  [type curve]
  (case type
    "ed25519" ["-t" "ed25519"]
    "ecdsa"   (if-let [bits (get curve-bits curve)]
                ["-t" "ecdsa" "-b" bits]
                (throw (ex-info (str "keypair-gen: unsupported curve " curve
                                     " (want one of "
                                     (str/join "," (sort (keys curve-bits)))
                                     ")")
                                {:curve curve})))
    (throw (ex-info (str "keypair-gen: unsupported type " type
                         " (want \"ed25519\" or \"ecdsa\")")
                    {:type type}))))

(defn build-script
  "Bash that:
     1. mktemp a 0700 dir
     2. ssh-keygen with -N \"\" -C comment into TMP/k
     3. move TMP/k -> $MU_SEALED_OUT_DIR/PRIVATE and TMP/k.pub -> .../PUBLIC
     4. chmod 600 both
     5. rm -rf TMP

   ssh-keygen overwrites without prompting because we point it at a path
   inside a fresh mktemp. We use -q to silence the 'Your identification
   has been saved' line so plugin stderr stays quiet."
  [type curve comment]
  (let [args (keygen-args type curve)]
    (str
     "set -eu\n"
     "if [ -z \"${MU_SEALED_OUT_DIR:-}\" ]; then\n"
     "  echo 'keypair-gen: MU_SEALED_OUT_DIR is unset' >&2\n"
     "  exit 1\n"
     "fi\n"
     "TMPDIR=$(mktemp -d)\n"
     "trap 'rm -rf \"$TMPDIR\"' EXIT\n"
     "chmod 700 \"$TMPDIR\"\n"
     "ssh-keygen -q -N '' "
     (str/join " " (map shell-quote args)) " "
     "-C " (shell-quote comment) " "
     "-f \"$TMPDIR/k\"\n"
     "mv \"$TMPDIR/k\" \"$MU_SEALED_OUT_DIR/PRIVATE\"\n"
     "mv \"$TMPDIR/k.pub\" \"$MU_SEALED_OUT_DIR/PUBLIC\"\n"
     "chmod 600 \"$MU_SEALED_OUT_DIR/PRIVATE\" \"$MU_SEALED_OUT_DIR/PUBLIC\"\n")))

(defn handle-plan [req]
  (let [target           (get req "target")
        tgt-name         (get target "name" "keypair")
        config           (get target "config" {})
        type             (get config "type" "ed25519")
        curve            (get config "curve" "P-256")
        comment          (get config "comment" tgt-name)
        sealed-out       (get target "sealed_outputs" {})
        sealed-out-modes (get target "sealed_output_modes" {})
        out-keys         (set (keys sealed-out))]

    (cond
      (empty? sealed-out)
      {"error" "keypair-gen: sealed_outputs is required (must declare PRIVATE and PUBLIC)"}

      (not= out-keys required-output-names)
      {"error" (str "keypair-gen: sealed_outputs keys must be exactly "
                    required-output-names " (got " out-keys ")")}

      :else
      (let [script (build-script type curve comment)
            action (cond-> {"id"             "generate"
                            "command"        ["bash" "-c" script]
                            "inputs"         {}
                            "outputs"        []
                            "depends_on"     []
                            "env"            {}
                            "network"        false
                            "sealed_outputs" sealed-out}
                     (seq sealed-out-modes) (assoc "sealed_output_modes" sealed-out-modes))]
        {"actions"          [action]
         "declared_outputs" {}}))))

(defn handle-request [req]
  (case (get req "method")
    "discover" (handle-discover)
    "plan"     (try (handle-plan req)
                    (catch Exception e {"error" (.getMessage e)}))
    {"error" (str "unknown method: " (get req "method"))}))

(loop []
  (when-let [line (read-line)]
    (println (json/generate-string (handle-request (json/parse-string line))))
    (flush)
    (recur)))
