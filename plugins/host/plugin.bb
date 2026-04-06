#!/usr/bin/env bb

;; Host observation plugin for mu.
;;
;; Capabilities: discover, observe.
;;
;; SSHs into a remote host and gathers system state: OS info, packages,
;; services, filesystems, network interfaces, and users. Returns structured
;; JSON for pudl to import.
;;
;; Config options:
;;   host     - hostname or IP address (required)
;;   user     - SSH user (default: "root")
;;   key      - path to SSH private key (optional, uses ssh-agent if absent)
;;   port     - SSH port (default: 22)
;;
;; Secrets (via sealed_inputs → secrets):
;;   SSH_PASS - SSH password (optional, used with sshpass if present)

(require '[cheshire.core :as json]
         '[babashka.process :as process]
         '[clojure.string :as str])

(defn handle-discover []
  {"name"             "host"
   "version"          "0.1.0"
   "protocol_version" 1
   "consumes"         []
   "produces"         ["host_state"]
   "capabilities"     ["discover" "observe"]
   "config_schema"    {"host" {"type" "string"}
                       "user" {"type" "string" "default" "root"}
                       "key"  {"type" "string"}
                       "port" {"type" "integer" "default" 22}}})

;;; ─── SSH helpers ──────────────────────────────────────────────────────

(defn check-sshpass!
  "Verify sshpass is installed when password auth is needed."
  []
  (let [result (process/sh ["which" "sshpass"])]
    (when-not (zero? (:exit result))
      (throw (ex-info "sshpass is required for password auth but not found. Install it: brew install hudochenkov/sshpass/sshpass (macOS) or apt install sshpass (Linux)" {})))))

(defn ssh-base-cmd
  "Build the SSH command vector from config and secrets."
  [config secrets]
  (let [host (get config "host")
        user (get config "user" "root")
        port (str (get config "port" 22))
        key  (get config "key")
        pass (get secrets "SSH_PASS")
        opts ["-o" "StrictHostKeyChecking=accept-new"
              "-o" "ConnectTimeout=10"
              "-p" port]
        opts (if key (into opts ["-i" key]) opts)
        dest (str user "@" host)]
    (if pass
      (do (check-sshpass!)
          (into ["sshpass" "-p" pass "ssh"] (conj opts dest)))
      (into ["ssh"] (into opts ["-o" "BatchMode=yes" dest])))))

;;; ─── Observe ─────────────────────────────────────────────────────────

(defn handle-observe [req]
  (let [target  (get req "target")
        config  (get target "config" {})
        secrets (get req "secrets" {})]
    (when-not (get config "host")
      (throw (ex-info "host plugin requires \"host\" in config" {})))
    (try
      (let [script   (slurp "gather.sh")
            ssh-cmd  (conj (ssh-base-cmd config secrets) "bash" "-s")
            result   (process/sh ssh-cmd {:in script})]
        (if (zero? (:exit result))
          (let [lines   (remove str/blank? (str/split-lines (:out result)))
                records (mapv #(json/parse-string %) lines)]
            {"current" {"records" records}})
          {"error" (str "remote gather failed (exit " (:exit result) "): "
                        (str/trim (:err result)))}))
      (catch Exception e
        {"error" (str "observe failed: " (.getMessage e))}))))

;;; ─── Dispatch ────────────────────────────────────────────────────────

(defn handle-request [req]
  (case (get req "method")
    "discover" (handle-discover)
    "observe"  (handle-observe req)
    {"error" (str "unknown method: " (get req "method"))}))

;; Main NDJSON loop
(loop []
  (when-let [line (read-line)]
    (let [req  (json/parse-string line)
          resp (handle-request req)]
      (println (json/generate-string resp))
      (flush)
      (recur))))
