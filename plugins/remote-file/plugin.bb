#!/usr/bin/env bb

;; remote-file: ensure a file on a remote host matches a local source file's
;; bytes, mode, and owner. Implements discover, plan, observe.
;;
;; Config:
;;   host         (string, required)
;;   user         (string, default "root")
;;   port         (int, default 22)
;;   path         (string, required — absolute remote path)
;;   mode         (string, optional — e.g. "0644")
;;   owner        (string, optional)
;;   group        (string, optional)
;;   make_parents (bool, default true)
;;   sudo         (bool, default false)
;;
;; Sources: exactly one file.
;; Sealed inputs: SSH_PASS (optional; required if sudo:true).

(require '[cheshire.core :as json]
         '[clojure.string :as str])

(defn handle-discover []
  {"name"             "remote-file"
   "version"          "0.1.0"
   "protocol_version" 1
   "description"      "Manage files on remote hosts over SSH with ownership and permissions"
   "consumes"         []
   "produces"         ["remote.file"]
   "capabilities"     ["discover" "plan" "observe"]})

;;; ─── Shell quoting ────────────────────────────────────────────────────

(defn shell-quote
  "Single-quote a string for safe inclusion in a bash command."
  [s]
  (str "'" (str/replace (str s) "'" "'\\''") "'"))

;;; ─── Remote-side bash scripts ─────────────────────────────────────────

(def write-script-body
  "Bash body executed on the remote via `bash -s`. Reads file bytes from
   stdin, writes to path, applies mode/owner/group, prints sha256.
   Expects RF_* env vars already exported by the caller (we prepend them
   to this script on the builder before piping to ssh)."
  (str
"set -eu\n"
"run() {\n"
"  if [ \"${RF_SUDO:-0}\" = '1' ]; then\n"
"    printf '%s\\n' \"${RF_SUDO_PASS:-}\" | sudo -S -p '' \"$@\"\n"
"  else\n"
"    \"$@\"\n"
"  fi\n"
"}\n"
"if [ \"${RF_MAKE_PARENTS:-1}\" = '1' ]; then\n"
"  DIR=$(dirname \"$RF_PATH\")\n"
"  run install -d \"$DIR\"\n"
"fi\n"
"TMP=$(mktemp)\n"
"cat > \"$TMP\"\n"
"run install -m \"${RF_MODE:-0644}\" \"$TMP\" \"$RF_PATH\"\n"
"rm -f \"$TMP\"\n"
"if [ -n \"${RF_OWNER:-}${RF_GROUP:-}\" ]; then\n"
"  OWN=\"${RF_OWNER:-}\"\n"
"  if [ -n \"${RF_GROUP:-}\" ]; then\n"
"    OWN=\"${OWN}:${RF_GROUP}\"\n"
"  fi\n"
"  run chown \"$OWN\" \"$RF_PATH\"\n"
"fi\n"
"sha256sum \"$RF_PATH\" | awk '{print $1}'\n"))

(def observe-script
  "Bash script that stats the remote file and emits JSON on stdout.
   Env: RF_PATH."
  (str
"set -eu\n"
"if [ ! -e \"$RF_PATH\" ]; then\n"
"  printf '{\"exists\":false}\\n'\n"
"  exit 0\n"
"fi\n"
"SHA=$(sha256sum \"$RF_PATH\" | awk '{print $1}')\n"
"MODE=$(stat -c '%a' \"$RF_PATH\")\n"
"OWNER=$(stat -c '%U' \"$RF_PATH\")\n"
"GROUP=$(stat -c '%G' \"$RF_PATH\")\n"
"printf '{\"exists\":true,\"sha256\":\"%s\",\"mode\":\"0%s\",\"owner\":\"%s\",\"group\":\"%s\"}\\n' \\\n"
"  \"$SHA\" \"$MODE\" \"$OWNER\" \"$GROUP\"\n"))

;;; ─── Validation ──────────────────────────────────────────────────────

(defn validate-target [target]
  (let [sources (get target "sources" [])
        config  (get target "config" {})]
    (cond
      (not= 1 (count sources))
      (throw (ex-info "remote-file requires exactly one source" {:sources sources}))
      (not (get config "host"))
      (throw (ex-info "remote-file requires config.host" {}))
      (not (get config "path"))
      (throw (ex-info "remote-file requires config.path" {}))
      :else nil)))

;;; ─── plan ─────────────────────────────────────────────────────────────

(defn build-export-lines
  "Build bash 'export VAR=value' lines for the RF_* env, fully quoted in
   Clojure (no bash-side escaping). Returns a concatenated string ending
   in a newline. RF_SUDO_PASS is injected at builder-wrapper time from
   $SSH_PASS when sudo:true, so it is NOT included here."
  [config]
  (let [pairs (cond-> [["RF_PATH"         (get config "path")]
                       ["RF_MAKE_PARENTS" (if (get config "make_parents" true) "1" "0")]
                       ["RF_SUDO"         (if (get config "sudo" false) "1" "0")]]
                (get config "mode")  (conj ["RF_MODE"  (get config "mode")])
                (get config "owner") (conj ["RF_OWNER" (get config "owner")])
                (get config "group") (conj ["RF_GROUP" (get config "group")]))]
    (str/join (for [[k v] pairs]
                (str "export " k "=" (shell-quote v) "\n")))))

(defn build-wrapper
  "Build the builder-side bash wrapper. It:
     1. Decides sshpass vs BatchMode ssh based on SSH_PASS presence.
     2. Constructs the remote script: exports (including RF_SUDO_PASS if
        sudo) + write-script body.
     3. Pipes '<exports><script><NUL><file-bytes>' — actually, since bash
        -s reads the exports+script from stdin along with the file bytes,
        we instead: export RF_* on the remote via ssh's remote command
        argument, then pipe file bytes as stdin to `bash -s`.

   But the plan's guidance is cleaner: generate ALL of the bash
   (exports + body + file-reading) on the builder, and pipe the file
   into ssh. The remote script reads its own env from the script prefix
   (already exported by the preceding lines) and then `cat > tmp`
   consumes the remaining stdin as file bytes. For this to work the
   local source file must be appended to the script stream as a
   here-doc-like trailer. That's fragile for binary files.

   Cleaner approach: ssh with a remote command that inlines the
   exports + the script, and pipe ONLY the file bytes to ssh's stdin.
   The exports and script body travel via the argv (ssh's remote
   command), which we build here with full single-quoting safety."
  [config src-path host user port sudo?]
  (let [exports     (build-export-lines config)
        sudo-export (if sudo?
                      "if [ -n \"${SSH_PASS:-}\" ]; then export RF_SUDO_PASS=\"$SSH_PASS\"; fi\n"
                      "")
        ;; Full remote script: sudo-export line (runs on remote; SSH_PASS
        ;; is not on remote — but we need RF_SUDO_PASS on remote). So
        ;; instead: build a separate builder-side prefix that prepends
        ;; "export RF_SUDO_PASS=..." (from $SSH_PASS) to the remote-cmd
        ;; just before passing it to ssh. We do that with printf %q.
        remote-script (str exports write-script-body)
        ;; Quote the whole remote script once, as a single argument to
        ;; `bash -c` on the remote. Pass it via the ssh remote-command
        ;; slot, which is joined by the remote shell as-is.
        quoted-script (shell-quote remote-script)
        ;; Builder-side wrapper:
        wrapper (str
                 "set -eu\n"
                 ;; Export SSHPASS so sshpass -e works
                 "if [ -n \"${SSH_PASS:-}\" ]; then export SSHPASS=\"$SSH_PASS\"; fi\n"
                 ;; Choose ssh variant
                 "if [ -n \"${SSH_PASS:-}\" ]; then\n"
                 "  SSH_CMD=(sshpass -e ssh)\n"
                 "else\n"
                 "  SSH_CMD=(ssh -o BatchMode=yes)\n"
                 "fi\n"
                 ;; Build remote command. For sudo, prepend
                 ;; "export RF_SUDO_PASS=<quoted SSH_PASS>;" to the bash
                 ;; -c argument.
                 "REMOTE_SCRIPT=" quoted-script "\n"
                 (if sudo?
                   (str "if [ -n \"${SSH_PASS:-}\" ]; then\n"
                        "  RF_SUDO_EXPORT=$(printf 'export RF_SUDO_PASS=%q\\n' \"$SSH_PASS\")\n"
                        "  REMOTE_SCRIPT=\"${RF_SUDO_EXPORT}\n${REMOTE_SCRIPT}\"\n"
                        "fi\n")
                   "")
                 ;; Build remote command as ONE argv element to ssh.
                 ;; Without this, ssh concatenates argv with spaces and the
                 ;; remote shell word-splits the script, breaking bash -c.
                 "REMOTE_CMD=\"bash -c $(printf %q \"$REMOTE_SCRIPT\")\"\n"
                 "cat " (shell-quote src-path) " | \"${SSH_CMD[@]}\" "
                 "-o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 "
                 "-p " (shell-quote port) " "
                 (shell-quote (str user "@" host)) " "
                 "\"$REMOTE_CMD\"\n")]
    wrapper))

(defn handle-plan [req]
  (let [target (get req "target")
        _      (validate-target target)
        config (get target "config" {})
        src    (first (get target "sources"))
        host   (get config "host")
        user   (get config "user" "root")
        port   (str (get config "port" 22))
        sudo?  (get config "sudo" false)
        sealed (get target "sealed_inputs" {})
        wrapper (build-wrapper config src host user port sudo?)]
    {"actions"
     [{"id"            "push"
       "command"       ["bash" "-c" wrapper]
       "inputs"        {"src" src}
       "outputs"       []
       "env"           (cond-> {"PATH" "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"}
                         (System/getenv "SSH_AUTH_SOCK") (assoc "SSH_AUTH_SOCK" (System/getenv "SSH_AUTH_SOCK"))
                         (System/getenv "HOME")          (assoc "HOME" (System/getenv "HOME")))
       "sealed_inputs" sealed
       "network"       true
       "impure"        true
       "depends_on"    []}]
     "declared_outputs" {"remote.file" (get config "path")}}))

;;; ─── observe ──────────────────────────────────────────────────────────

(defn ssh-argv
  "Build the ssh argv (without a trailing remote command) from config.
   When SSH_PASS is present, wraps with sshpass for password auth.
   When absent, uses BatchMode=yes (agent-only)."
  [config has-pass?]
  (let [host (get config "host")
        user (get config "user" "root")
        port (str (get config "port" 22))
        opts ["-o" "StrictHostKeyChecking=accept-new"
              "-o" "ConnectTimeout=10"
              "-p" port]
        opts (if has-pass? opts (into opts ["-o" "BatchMode=yes"]))
        dest (str user "@" host)
        base (if has-pass?
               ["sshpass" "-e" "ssh"]
               ["ssh"])]
    (into base (conj opts dest))))

(defn run-sh [argv opts]
  (let [{:keys [in env]} opts
        pb (ProcessBuilder. (into-array String argv))]
    (when env
      (let [e (.environment pb)]
        (doseq [[k v] env] (.put e k v))))
    (.redirectErrorStream pb false)
    (let [proc   (.start pb)
          stdin  (.getOutputStream proc)
          stdout (.getInputStream proc)
          stderr (.getErrorStream proc)]
      (when in
        (.write stdin (.getBytes in "UTF-8"))
        (.close stdin))
      (let [out (slurp stdout)
            err (slurp stderr)
            ex  (.waitFor proc)]
        {:exit ex :out out :err err}))))

(defn handle-observe [req]
  (try
    (let [target   (get req "target")
          _        (validate-target target)
          config   (get target "config" {})
          secrets  (get req "secrets" {})
          path     (get config "path")
          pass     (get secrets "SSH_PASS")
          ssh-argv (ssh-argv config (boolean pass))
          argv     (conj (vec ssh-argv) "env" (str "RF_PATH=" path) "bash" "-s")
          env      (cond-> {} pass (assoc "SSHPASS" pass))
          result   (run-sh argv {:in observe-script :env env})]
      (if (zero? (:exit result))
        (let [state (json/parse-string (str/trim (:out result)))]
          {"current"
           {"records"
            [(merge {"_schema" "remote.file"
                     "host"    (get config "host")
                     "path"    path}
                    state)]}})
        {"error" (str "observe failed (exit " (:exit result) "): "
                      (str/trim (:err result)))}))
    (catch Exception e
      {"error" (str "observe failed: " (.getMessage e))})))

;;; ─── dispatch ────────────────────────────────────────────────────────

(defn handle-request [req]
  (case (get req "method")
    "discover" (handle-discover)
    "plan"     (try (handle-plan req)
                    (catch Exception e {"error" (.getMessage e)}))
    "observe"  (handle-observe req)
    {"error" (str "unknown method: " (get req "method"))}))

(loop []
  (when-let [line (read-line)]
    (println (json/generate-string (handle-request (json/parse-string line))))
    (flush)
    (recur)))
