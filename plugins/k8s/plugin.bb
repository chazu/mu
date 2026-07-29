#!/usr/bin/env bb

;; Kubernetes convergence plugin for mu.
;;
;; Converges Kubernetes resources via kubectl apply. Observes drift via
;; structured comparison of desired manifests against live cluster state.
;; All actions are impure (side effects on the cluster) and require
;; network access.
;;
;; Config options:
;;   namespace             - Kubernetes namespace (default: from manifest)
;;   context               - kubectl context (default: current context)
;;   kubeconfig            - path to kubeconfig (default: ~/.kube/config)
;;   server_side           - use server-side apply (default: true)
;;   prune                 - prune resources not in manifest (default: false)
;;   dry_run               - kubectl --dry-run=server (default: false)
;;   ignore_paths          - list of dot-separated field paths to ignore in drift
;;                           detection (e.g. ["spec.replicas" "metadata.annotations.my-ann"])
;;   inventory              - optional inventory mode config:
;;                           {"kinds" ["pods" "deployments"]
;;                            "namespace" "default" | "all_namespaces" true}
;;   sealed_output_secrets - map sealed_output NAME -> {"namespace","secret","key"}.
;;                           After apply, the fetch-secrets action runs
;;                           `kubectl get secret -n NS NAME -o jsonpath="{.data.KEY}"
;;                           | base64 -d` for each entry and writes the decoded
;;                           value to $MU_SEALED_OUT_DIR/NAME (0600). Use to
;;                           capture ServiceAccount tokens, generated database
;;                           credentials, or any cluster-side secret value into
;;                           a secret backend. Keys must match the target's
;;                           sealed_outputs map.
;;
;; Sealed inputs are forwarded to every action (apply, fetch-secrets). Use
;; sealed_input_modes:file for kubeconfig and other multi-line secrets so
;; the runner writes them to a 0600 temp file and exports $NAME as the path
;; (kubectl reads them via --kubeconfig "$NAME" or KUBECONFIG="$NAME").

(require '[cheshire.core :as json]
         '[clojure.string :as str]
         '[clj-yaml.core :as yaml]
         '[clojure.data :as data]
         '[babashka.process :as process])

;;; ─── Discover ───────────────────────────────────────────────────────

(defn handle-discover []
  {"name"             "k8s"
   "version"          "0.3.0"
   "protocol_version" 1
   "description"      "Apply Kubernetes manifests via kubectl with server-side apply and drift detection"
   "capabilities"     ["discover" "plan" "observe"]
   "consumes"         ["source:yaml" "source:json"]
   "produces"         ["k8s_resource"]
   "config_schema"    {"namespace"             {"type" "string" "description" "Target Kubernetes namespace"}
                       "context"               {"type" "string" "description" "Kubeconfig context to use"}
                       "kubeconfig"            {"type" "string" "description" "Path to kubeconfig file"}
                       "server_side"           {"type" "boolean" "default" true "description" "Use server-side apply"}
                       "prune"                 {"type" "boolean" "default" false "description" "Remove resources not in the manifest set"}
                       "dry_run"               {"type" "boolean" "default" false "description" "Validate without applying changes"}
                       "ignore_paths"          {"type" "array" "items" {"type" "string"}
                                                "default" [] "description" "JSON paths to ignore during drift detection"}
                       "inventory"             {"type" "object" "description" "Read live cluster objects instead of diffing declared manifests"
                                                "properties" {"kinds" {"type" "array" "items" {"type" "string"}}
                                                              "namespace" {"type" "string"}
                                                              "all_namespaces" {"type" "boolean"}}}
                       "sealed_output_secrets" {"type" "object" "description" "Map of sealed output names to cluster secret references"}}})

(defn shell-quote
  "Single-quote a string for safe inclusion in a bash command."
  [s]
  (str "'" (str/replace (str s) "'" "'\\''") "'"))

;;; ─── Helpers ────────────────────────────────────────────────────────

(defn target-short-name [target-name]
  (last (str/split target-name #"[:/]")))

(defn kubectl-base-args
  "Build common kubectl flags from config."
  [config]
  (cond-> []
    (get config "namespace")  (conj "--namespace" (get config "namespace"))
    (get config "context")    (conj "--context" (get config "context"))
    (get config "kubeconfig") (conj "--kubeconfig" (get config "kubeconfig"))))

(defn inventory-config
  "Validate and normalize the optional live inventory configuration."
  [config]
  (when-let [inventory (get config "inventory")]
    (let [kinds (get inventory "kinds")]
      (when (or (not (sequential? kinds)) (empty? kinds)
                (some #(or (not (string? %)) (str/blank? %)) kinds))
        (throw (ex-info "k8s inventory requires a non-empty kinds list" {})))
      (when (and (get inventory "all_namespaces")
                 (or (get inventory "namespace") (get config "namespace")))
        (throw (ex-info "k8s inventory cannot combine all_namespaces with namespace" {})))
      (assoc inventory "kinds" (vec kinds)))))

(defn inventory-kubectl-args
  "Build kubectl flags for live inventory without duplicating namespace flags."
  [config inventory]
  (let [base-config (dissoc config "namespace")
        namespace   (or (get inventory "namespace") (get config "namespace"))]
    (cond-> (kubectl-base-args base-config)
      namespace (conj "--namespace" namespace)
      (get inventory "all_namespaces") (conj "--all-namespaces"))))

(defn fetch-inventory-kind
  "Return the API objects for one kind, preserving the server's object shape."
  [base-args kind]
  (let [result (process/sh (into ["kubectl" "get" kind] (concat base-args ["-o" "json"])))]
    (if (zero? (:exit result))
      (let [body (json/parse-string (:out result) true)]
        (or (:items body) []))
      (throw (ex-info (str "kubectl get " kind " failed: " (str/trim (:err result)))
                      {:kind kind :exit (:exit result)})))))

(defn inventory-record
  "Attach the stable PUDL/mu resource type to a live Kubernetes object."
  [object]
  (assoc object "_schema" "k8s.resource"))

(defn handle-inventory-observe
  "Observe live objects without requiring manifest sources."
  [target config]
  (let [inventory (inventory-config config)
        base-args (inventory-kubectl-args config inventory)
        records   (mapcat #(map inventory-record (fetch-inventory-kind base-args %))
                          (get inventory "kinds"))]
    {"current" {"records" (vec records)}}))

;;; ─── Plan ───────────────────────────────────────────────────────────

(defn validate-secret-spec
  "Each sealed_output_secrets value must be a map with namespace, secret, key."
  [name spec]
  (let [missing (remove #(get spec %) ["namespace" "secret" "key"])]
    (when (seq missing)
      (throw (ex-info
              (str "k8s: sealed_output_secrets[" name
                   "] missing required fields: " (str/join "," missing))
              {:name name :spec spec})))))

(defn build-fetch-secrets-cmd
  "Bash one-liner: kubectl get secret per entry, base64-decode, write to
   $MU_SEALED_OUT_DIR/NAME (chmod 0600). Common kubectl base flags
   (context/kubeconfig) come from `base-args`; --namespace is per-secret."
  [base-args sealed-out-secrets]
  (let [pairs   (sort-by key sealed-out-secrets)
        kctl    (str/join " "
                          (cons "kubectl"
                                (map shell-quote base-args)))]
    (str "set -eu\n"
         "if [ -z \"${MU_SEALED_OUT_DIR:-}\" ]; then\n"
         "  echo 'k8s: sealed_output_secrets declared but MU_SEALED_OUT_DIR is unset' >&2\n"
         "  exit 1\n"
         "fi\n"
         (str/join
          (for [[name spec] pairs
                :let [ns  (get spec "namespace")
                      sec (get spec "secret")
                      key (get spec "key")]]
            (str kctl " get secret -n " (shell-quote ns) " "
                 (shell-quote sec) " "
                 "-o " (shell-quote (str "jsonpath={.data." key "}")) " "
                 "| base64 -d > \"$MU_SEALED_OUT_DIR/" name "\"\n"
                 "chmod 600 \"$MU_SEALED_OUT_DIR/" name "\"\n"))))))

(defn attach-sealed-inputs
  "Add sealed_inputs / sealed_input_modes to an action when present."
  [action sealed sealed-modes]
  (cond-> action
    (seq sealed)        (assoc "sealed_inputs" sealed)
    (seq sealed-modes)  (assoc "sealed_input_modes" sealed-modes)))

(defn handle-plan [req]
  (let [target            (get req "target")
        tgt-name          (get target "name")
        sources           (get target "sources" [])
        config            (get target "config" {})
        sealed            (get target "sealed_inputs" {})
        sealed-modes      (get target "sealed_input_modes" {})
        sealed-out        (get target "sealed_outputs" {})
        sealed-out-modes  (get target "sealed_output_modes" {})
        sealed-out-secs   (get config "sealed_output_secrets" {})
        out-keys          (set (keys sealed-out))
        secret-keys       (set (keys sealed-out-secs))]

    (cond
      (and (seq sealed-out) (not= out-keys secret-keys))
      {"error" (str "k8s: sealed_outputs keys " out-keys
                    " must match config.sealed_output_secrets keys " secret-keys)}

      :else
      (do
        (doseq [[name spec] sealed-out-secs]
          (validate-secret-spec name spec))
        (let [;; Build input map from declared sources (manifest files)
              inputs (into {} (map (fn [s] [s s]) sources))

              ;; Build kubectl apply command
              base-args (kubectl-base-args config)
              apply-cmd (-> (into ["kubectl" "apply"] base-args)
                            (cond->
                              (get config "server_side" true) (conj "--server-side")
                              (get config "prune" false)      (conj "--prune")
                              (get config "dry_run" false)    (conj "--dry-run=server"))
                            ;; Add all manifest files with -f
                            (into (mapcat (fn [s] ["-f" s]) sources)))

              apply-action (-> {"id"         "apply"
                                "command"    apply-cmd
                                "inputs"     inputs
                                "outputs"    []
                                "depends_on" []
                                "env"        {}
                                "network"    true
                                "impure"     true}
                               (attach-sealed-inputs sealed sealed-modes))

              fetch-action (when (seq sealed-out)
                             (cond-> (-> {"id"             "fetch-secrets"
                                          "command"        ["bash" "-c"
                                                            (build-fetch-secrets-cmd base-args sealed-out-secs)]
                                          "inputs"         {}
                                          "outputs"        []
                                          "depends_on"     ["apply"]
                                          "env"            {}
                                          "network"        true
                                          "impure"         true
                                          "sealed_outputs" sealed-out}
                                         (attach-sealed-inputs sealed sealed-modes))
                               (seq sealed-out-modes) (assoc "sealed_output_modes" sealed-out-modes)))

              actions (cond-> [apply-action]
                        fetch-action (conj fetch-action))]

          {"actions"          actions
           "declared_outputs" {}})))))

;;; ─── Observe: structured drift detection ────────────────────────────

;; Well-known metadata paths that always differ between desired and live.
;; These are set by the API server and are never part of the user's intent.
(def well-known-noise-paths
  [[:metadata :managedFields]
   [:metadata :annotations (keyword "kubectl.kubernetes.io/last-applied-configuration")]
   [:metadata :resourceVersion]
   [:metadata :uid]
   [:metadata :creationTimestamp]
   [:metadata :generation]
   [:metadata :selfLink]
   [:status]])

(defn dissoc-in
  "Remove a nested key from a map. If the parent becomes empty, remove it too."
  [m [k & ks]]
  (if ks
    (if-let [inner (get m k)]
      (let [result (dissoc-in inner ks)]
        (if (and (map? result) (empty? result))
          (dissoc m k)
          (assoc m k result)))
      m)
    (dissoc m k)))

(defn strip-noise
  "Remove well-known server-set fields from a live object."
  [obj]
  (reduce dissoc-in obj well-known-noise-paths))

(defn extract-fieldsv1-paths
  "Walk a fieldsV1 structure and return a set of keyword-path vectors.
   fieldsV1 uses 'f:fieldName' keys for fields and 'k:{...}' for list items."
  ([fields] (extract-fieldsv1-paths fields []))
  ([fields prefix]
   (reduce-kv
     (fn [acc k v]
       (let [kname (name k)]
         (cond
           ;; f: prefix = field name
           (str/starts-with? kname "f:")
           (let [field-name (keyword (subs kname 2))
                 path (conj prefix field-name)]
             (if (and (map? v) (seq v))
               (into acc (extract-fieldsv1-paths v path))
               (conj acc path)))
           ;; Skip k: entries (list item keys) — too complex for now
           :else acc)))
     #{} (or fields {}))))

(defn server-managed-paths
  "Extract field paths managed by controllers other than kubectl.
   These are fields we don't own and should ignore during diff."
  [managed-fields]
  (let [user-managers #{"kubectl" "kubectl-client-side-apply"}
        server-entries (remove #(user-managers (:manager %)) managed-fields)]
    (->> server-entries
         (mapcat #(extract-fieldsv1-paths (:fieldsV1 %)))
         set)))

(defn select-keys-deep
  "Recursively project `live` down to only the keys present in `desired`.
   This is the core of desired-state-only comparison: we only check fields
   the user declared, ignoring everything else the server added."
  [live desired]
  (cond
    (and (map? desired) (map? live))
    (reduce-kv
      (fn [acc k v]
        (if (contains? live k)
          (assoc acc k (select-keys-deep (get live k) v))
          acc))
      {} desired)

    (and (sequential? desired) (sequential? live))
    (vec (map select-keys-deep live desired))

    :else live))

(defn parse-ignore-path
  "Parse a dot-separated path string into a keyword vector.
   e.g. 'spec.replicas' -> [:spec :replicas]"
  [s]
  (mapv keyword (str/split s #"\.")))

(defn strip-user-ignores
  "Remove user-configured ignore paths from an object."
  [obj ignore-paths]
  (reduce (fn [o path] (dissoc-in o (parse-ignore-path path)))
          obj ignore-paths))

(defn parse-source-manifests
  "Parse YAML/JSON source files into a flat seq of resource maps.
   Handles multi-document YAML (--- separators)."
  [sources]
  (->> sources
       (mapcat (fn [path]
                 (let [content (slurp path)]
                   (if (str/ends-with? path ".json")
                     [(json/parse-string content true)]
                     (yaml/parse-string content :load-all true)))))
       (remove nil?)))

(defn resource-id
  "Extract identifying fields from a parsed manifest."
  [manifest]
  {:kind      (:kind manifest)
   :name      (get-in manifest [:metadata :name])
   :namespace (get-in manifest [:metadata :namespace])})

(defn fetch-live-object
  "Fetch a live resource from the cluster as a parsed map with keyword keys.
   Returns nil if the resource does not exist."
  [base-args {:keys [kind name namespace]}]
  (let [cmd (cond-> ["kubectl" "get" kind name]
              namespace (into ["--namespace" namespace])
              true      (into base-args)
              true      (conj "-o" "json"))
        result (process/sh cmd)]
    (when (= 0 (:exit result))
      (json/parse-string (:out result) true))))

(defn diff-resource
  "Compare a desired manifest against the live cluster state.
   Returns {:drifted? bool, :desired-only map-or-nil, :live-only map-or-nil}."
  [desired live config]
  (let [ignore-paths (get config "ignore_paths" [])
        ;; 1. Strip well-known noise from live
        live-clean (strip-noise live)
        ;; 2. Use managedFields to find server-managed paths
        server-paths (server-managed-paths (:managedFields (:metadata live)))
        live-clean (reduce dissoc-in live-clean server-paths)
        ;; 3. Strip user-configured ignore paths from both sides
        live-clean (strip-user-ignores live-clean ignore-paths)
        desired-clean (strip-user-ignores desired ignore-paths)
        ;; 4. Project live down to only desired keys
        live-projected (select-keys-deep live-clean desired-clean)
        ;; 5. Structural diff
        [only-desired only-live _both] (data/diff desired-clean live-projected)]
    {:desired-only only-desired
     :live-only    only-live
     :drifted?     (or (some? only-desired) (some? only-live))}))

(defn format-path
  "Format a nested map diff as flat dotted-path lines."
  [prefix m label]
  (when (map? m)
    (mapcat (fn [[k v]]
              (let [p (str prefix (name k))]
                (if (map? v)
                  (format-path (str p ".") v label)
                  [(str "  " label " " p ": " (pr-str v))])))
            (sort-by first m))))

(defn format-diff
  "Format a resource diff as human-readable text."
  [rid {:keys [desired-only live-only]}]
  (let [header (str (:kind rid) "/" (:name rid) ":")
        lines  (concat
                 (format-path "" desired-only "-")
                 (format-path "" live-only "+"))]
    (str/join "\n" (cons header lines))))

(defn handle-observe [req]
  (let [target    (get req "target")
        sources   (get target "sources" [])
        config    (get target "config" {})
        base-args (kubectl-base-args config)]
    (try
      (if (get config "inventory")
        (handle-inventory-observe target config)
        (let [manifests (parse-source-manifests sources)
            resources (for [manifest manifests
                           :let [rid  (resource-id manifest)
                                 live (fetch-live-object base-args rid)]]
                        (if (nil? live)
                          {"resource" (str (:kind rid) "/" (:name rid))
                           "exists"   false}
                          (let [d (diff-resource manifest live config)]
                            (cond-> {"resource"  (str (:kind rid) "/" (:name rid))
                                     "exists"    true
                                     "matches"   (not (:drifted? d))}
                              (:drifted? d)
                              (assoc "diff" (format-diff rid d))))))]
          {"current" {"resources" (vec resources)}}))
      (catch Exception e
        {"error" (str "observe failed: " (.getMessage e))}))))

;;; ─── Dispatch ───────────────────────────────────────────────────────

(defn handle-request [req]
  (case (get req "method")
    "discover" (handle-discover)
    "plan"     (try (handle-plan req)
                    (catch Exception e {"error" (.getMessage e)}))
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
