#!/usr/bin/env bb

;; remote-exec: run a command on a remote host via SSH, optionally guarded
;; by a check command. Implements discover and plan (no observe).
;;
;; Config:
;;   host                (string, required)
;;   user                (string, default "root")
;;   port                (int, default 22)
;;   command             ([]string, required — argv executed remotely)
;;   check               ([]string, optional — if exits 0, command is skipped)
;;   env                 (map[string]string, optional — exported before command)
;;   work_dir            (string, optional — cd before command)
;;   sudo                (bool, default false)
;;   impure              (bool, default true)
;;   sealed_output_files (map[string]string, optional — sealed_output NAME ->
;;                        absolute remote path. After the remote command
;;                        succeeds, the wrapper ssh-cats each remote path
;;                        into $MU_SEALED_OUT_DIR/NAME (0600) so the runner
;;                        can route the bytes through store_secret. Keys
;;                        must match the target's sealed_outputs map.)
;;
;; Sealed inputs:  SSH_PASS (required when sudo:true).
;; Sealed outputs: see sealed_output_files above. The remote command is
;;                 responsible for *producing* each declared file; the
;;                 wrapper only fetches and forwards.

(require '[cheshire.core :as json]
         '[clojure.string :as str])

(defn handle-discover []
  {"name"             "remote-exec"
   "version"          "0.2.0"
   "protocol_version" 1
   "consumes"         []
   "produces"         []
   "capabilities"     ["discover" "plan"]})

;;; ─── Shell quoting ────────────────────────────────────────────────────

(defn shell-quote
  "Single-quote a string for safe inclusion in a bash command."
  [s]
  (str "'" (str/replace (str s) "'" "'\\''") "'"))

;;; ─── Remote-side bash script ──────────────────────────────────────────

(defn build-remote-script
  "Render a bash snippet that runs on the remote host.
   forward-keys: sealed_input env var names (minus SSH_PASS) to forward
   through sudo via --preserve-env."
  [config forward-keys]
  (let [cmd       (get config "command")
        check     (get config "check")
        env       (get config "env" {})
        work-dir  (get config "work_dir")
        sudo?     (get config "sudo" false)
        env-lines (for [[k v] env]
                    (str "export " k "=" (shell-quote v) "\n"))
        cd-line   (when work-dir
                    (str "cd " (shell-quote work-dir) "\n"))
        argv->str (fn [argv] (str/join " " (map shell-quote argv)))
        preserve  (when (seq forward-keys)
                    (str "--preserve-env=" (str/join "," forward-keys) " "))
        run-prefix (if sudo?
                     (str "printf '%s\\n' \"${RE_SUDO_PASS:-}\" | "
                          "sudo -S -p '' " (or preserve ""))
                     "")
        check-line (when check
                     (str "if " (argv->str check) " >/dev/null 2>&1; then "
                          "echo '[check passed, skipping]'; exit 0; fi\n"))
        cmd-line  (str run-prefix (argv->str cmd) "\n")]
    (str "set -eu\n"
         (str/join env-lines)
         (or cd-line "")
         (or check-line "")
         cmd-line)))

;;; ─── Validation ──────────────────────────────────────────────────────

(defn validate-target [target]
  (let [config        (get target "config" {})
        sealed-out    (get target "sealed_outputs" {})
        sealed-files  (get config "sealed_output_files" {})
        out-keys      (set (keys sealed-out))
        file-keys     (set (keys sealed-files))]
    (cond
      (not (get config "host"))
      (throw (ex-info "remote-exec requires config.host" {}))
      (empty? (get config "command"))
      (throw (ex-info "remote-exec requires config.command" {}))
      (and (seq sealed-out) (not= out-keys file-keys))
      (throw (ex-info
              (str "remote-exec: sealed_outputs keys " out-keys
                   " must match config.sealed_output_files keys " file-keys)
              {:sealed_outputs out-keys :sealed_output_files file-keys}))
      :else nil)))

;;; ─── Builder-side wrapper ────────────────────────────────────────────

(defn build-sealed-output-fetchers
  "Emit bash lines that, after the remote command succeeds, ssh-cat each
   sealed_output file from the remote into $MU_SEALED_OUT_DIR/NAME (0600)
   so the runner can route bytes through store_secret. Returns \"\" when
   no sealed outputs are declared."
  [sealed-files]
  (if (empty? sealed-files)
    ""
    (str
     "if [ -z \"${MU_SEALED_OUT_DIR:-}\" ]; then\n"
     "  echo 'remote-exec: sealed_output_files declared but MU_SEALED_OUT_DIR is unset' >&2\n"
     "  exit 1\n"
     "fi\n"
     (str/join
      (for [[name remote-path] sealed-files]
        (str "\"${SSH_CMD[@]}\" "
             "-o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 "
             "-p \"$REMOTE_PORT\" \"$REMOTE_DEST\" "
             "cat " (shell-quote remote-path) " "
             "> \"$MU_SEALED_OUT_DIR/" name "\"\n"
             "chmod 600 \"$MU_SEALED_OUT_DIR/" name "\"\n"))))))

(defn build-wrapper
  "Build the bash wrapper executed on the builder. It:
     1. Chooses `sshpass -e ssh` vs `ssh -o BatchMode=yes` based on
        $SSH_PASS.
     2. Forwards all sealed_input env vars (except SSH_PASS, reserved
        for ssh auth) to the remote by prepending `export K=<printf %q>`
        lines to the remote script.
     3. When sudo:true, also prepends RE_SUDO_PASS derived from SSH_PASS.
     4. Passes the remote command as one argv element to ssh to avoid
        remote shell word-splitting.
     5. After the remote command succeeds, ssh-cats each
        sealed_output_files entry into $MU_SEALED_OUT_DIR/NAME (0600)
        so the runner can route bytes through store_secret."
  [config host user port sudo? sealed-keys sealed-files]
  (let [forward-keys   (remove #(= "SSH_PASS" %) sealed-keys)
        remote-script  (build-remote-script config forward-keys)
        quoted-script  (shell-quote remote-script)
        forward-lines  (str/join
                        (for [k forward-keys]
                          (str "if [ -n \"${" k ":-}\" ]; then\n"
                               "  _RE_EXPORT=$(printf 'export " k "=%q\\n' \"$" k "\")\n"
                               "  REMOTE_SCRIPT=\"${_RE_EXPORT}\n${REMOTE_SCRIPT}\"\n"
                               "fi\n")))]
    (str
     "set -eu\n"
     "if [ -n \"${SSH_PASS:-}\" ]; then export SSHPASS=\"$SSH_PASS\"; fi\n"
     "if [ -n \"${SSH_PASS:-}\" ]; then\n"
     "  SSH_CMD=(sshpass -e ssh)\n"
     "else\n"
     "  SSH_CMD=(ssh -o BatchMode=yes)\n"
     "fi\n"
     "REMOTE_SCRIPT=" quoted-script "\n"
     forward-lines
     (if sudo?
       (str "if [ -n \"${SSH_PASS:-}\" ]; then\n"
            "  RE_SUDO_EXPORT=$(printf 'export RE_SUDO_PASS=%q\\n' \"$SSH_PASS\")\n"
            "  REMOTE_SCRIPT=\"${RE_SUDO_EXPORT}\n${REMOTE_SCRIPT}\"\n"
            "fi\n")
       "")
     "REMOTE_CMD=\"bash -c $(printf %q \"$REMOTE_SCRIPT\")\"\n"
     "REMOTE_PORT=" (shell-quote port) "\n"
     "REMOTE_DEST=" (shell-quote (str user "@" host)) "\n"
     "\"${SSH_CMD[@]}\" "
     "-o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 "
     "-p \"$REMOTE_PORT\" \"$REMOTE_DEST\" "
     "\"$REMOTE_CMD\"\n"
     (build-sealed-output-fetchers sealed-files))))

;;; ─── plan ─────────────────────────────────────────────────────────────

(defn handle-plan [req]
  (let [target       (get req "target")
        _            (validate-target target)
        config       (get target "config" {})
        host         (get config "host")
        user         (get config "user" "root")
        port         (str (get config "port" 22))
        sudo?        (get config "sudo" false)
        impure?      (get config "impure" true)
        sealed       (get target "sealed_inputs" {})
        sealed-modes (get target "sealed_input_modes" {})
        sealed-out   (get target "sealed_outputs" {})
        sealed-out-m (get target "sealed_output_modes" {})
        sealed-files (get config "sealed_output_files" {})
        wrapper      (build-wrapper config host user port sudo?
                                    (keys sealed) sealed-files)]
    {"actions"
     [(cond-> {"id"            "exec"
               "command"       ["bash" "-c" wrapper]
               "inputs"        {}
               "outputs"       []
               "env"           {"PATH" "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"}
               "sealed_inputs" sealed
               "network"       true
               "impure"        impure?
               "depends_on"    []}
        (seq sealed-modes) (assoc "sealed_input_modes"  sealed-modes)
        (seq sealed-out)   (assoc "sealed_outputs"      sealed-out)
        (seq sealed-out-m) (assoc "sealed_output_modes" sealed-out-m))]
     "declared_outputs" {}}))

;;; ─── dispatch ────────────────────────────────────────────────────────

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
