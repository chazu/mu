#!/usr/bin/env bb

;; Kubernetes convergence plugin for mu.
;;
;; Converges Kubernetes resources via kubectl apply. Observes drift via
;; kubectl diff. All actions are impure (side effects on the cluster)
;; and require network access.
;;
;; Config options:
;;   namespace   - Kubernetes namespace (default: from manifest)
;;   context     - kubectl context (default: current context)
;;   kubeconfig  - path to kubeconfig (default: ~/.kube/config)
;;   server_side - use server-side apply (default: true)
;;   prune       - prune resources not in manifest (default: false)
;;   dry_run     - kubectl --dry-run=server (default: false)

(require '[cheshire.core :as json]
         '[clojure.string :as str]
         '[babashka.process :as process])

(defn handle-discover []
  {"name"             "k8s"
   "version"          "0.1.0"
   "protocol_version" 1
   "capabilities"     ["discover" "plan" "observe"]
   "consumes"         ["source:yaml" "source:json"]
   "produces"         ["k8s_resource"]
   "config_schema"    {"namespace"   {"type" "string"}
                       "context"     {"type" "string"}
                       "kubeconfig"  {"type" "string"}
                       "server_side" {"type" "boolean" "default" true}
                       "prune"       {"type" "boolean" "default" false}
                       "dry_run"     {"type" "boolean" "default" false}}})

(defn target-short-name [target-name]
  (last (str/split target-name #"[:/]")))

(defn kubectl-base-args
  "Build common kubectl flags from config."
  [config]
  (cond-> []
    (get config "namespace")  (conj "--namespace" (get config "namespace"))
    (get config "context")    (conj "--context" (get config "context"))
    (get config "kubeconfig") (conj "--kubeconfig" (get config "kubeconfig"))))

(defn handle-plan [req]
  (let [target   (get req "target")
        tgt-name (get target "name")
        sources  (get target "sources" [])
        config   (get target "config" {})

        ;; Build input map from declared sources (manifest files)
        inputs (into {} (map (fn [s] [s s]) sources))

        ;; Build kubectl apply command
        base-args (kubectl-base-args config)
        apply-cmd (-> (into ["kubectl" "apply"] base-args)
                      (cond->
                        (get config "server_side" true) (conj "--server-side")
                        (get config "prune" false)      (conj "--prune")
                        (get config "dry_run" false)    (conj "--dry-run=server"))
                      ;; Add all manifest files with -f
                      (into (mapcat (fn [s] ["-f" s]) sources)))]

    {"actions"
     [{"id"         "apply"
       "command"    apply-cmd
       "inputs"     inputs
       "outputs"    []
       "depends_on" []
       "env"        {}
       "network"    true
       "impure"     true}]
     "declared_outputs" {}}))

(defn handle-observe [req]
  (let [target   (get req "target")
        sources  (get target "sources" [])
        config   (get target "config" {})
        base-args (kubectl-base-args config)

        ;; Build kubectl diff command
        diff-cmd (-> (into ["kubectl" "diff"] base-args)
                     (into (mapcat (fn [s] ["-f" s]) sources)))]

    (try
      (let [result (process/sh diff-cmd)]
        (cond
          ;; Exit 0 = no diff = converged
          (= 0 (:exit result))
          {"state" "converged"}

          ;; Exit 1 = diff exists = drifted
          (= 1 (:exit result))
          {"state" "drifted"
           "diff"  (str (:out result))}

          ;; Exit >1 = error (resource doesn't exist, auth failure, etc.)
          :else
          {"state" "drifted"
           "diff"  (str "kubectl diff exited with code " (:exit result) "\n"
                        (:err result))}))
      (catch Exception e
        {"state" "drifted"
         "diff"  (str "kubectl diff failed: " (.getMessage e))}))))

(defn handle-request [req]
  (case (get req "method")
    "discover" (handle-discover)
    "plan"     (handle-plan req)
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
