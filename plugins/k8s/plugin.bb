#!/usr/bin/env bb

;; Kubernetes convergence plugin for mu.
;;
;; Converges Kubernetes resources via kubectl apply. Observes drift via
;; structured comparison of desired manifests against live cluster state.
;; All actions are impure (side effects on the cluster) and require
;; network access.
;;
;; Config options:
;;   namespace    - Kubernetes namespace (default: from manifest)
;;   context      - kubectl context (default: current context)
;;   kubeconfig   - path to kubeconfig (default: ~/.kube/config)
;;   server_side  - use server-side apply (default: true)
;;   prune        - prune resources not in manifest (default: false)
;;   dry_run      - kubectl --dry-run=server (default: false)
;;   ignore_paths - list of dot-separated field paths to ignore in drift
;;                  detection (e.g. ["spec.replicas" "metadata.annotations.my-ann"])

(require '[cheshire.core :as json]
         '[clojure.string :as str]
         '[clj-yaml.core :as yaml]
         '[clojure.data :as data]
         '[babashka.process :as process])

;;; ─── Discover ───────────────────────────────────────────────────────

(defn handle-discover []
  {"name"             "k8s"
   "version"          "0.2.0"
   "protocol_version" 1
   "capabilities"     ["discover" "plan" "observe"]
   "consumes"         ["source:yaml" "source:json"]
   "produces"         ["k8s_resource"]
   "config_schema"    {"namespace"    {"type" "string"}
                       "context"      {"type" "string"}
                       "kubeconfig"   {"type" "string"}
                       "server_side"  {"type" "boolean" "default" true}
                       "prune"        {"type" "boolean" "default" false}
                       "dry_run"      {"type" "boolean" "default" false}
                       "ignore_paths" {"type" "array" "items" {"type" "string"}
                                       "default" []}}})

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

;;; ─── Plan ───────────────────────────────────────────────────────────

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
      (let [manifests (parse-source-manifests sources)
            diffs (for [manifest manifests
                        :let [rid  (resource-id manifest)
                              live (fetch-live-object base-args rid)]]
                    (if (nil? live)
                      {:resource rid
                       :drifted? true
                       :message  (str (:kind rid) "/" (:name rid)
                                      " does not exist on cluster")}
                      (assoc (diff-resource manifest live config)
                             :resource rid)))
            any-drifted? (some :drifted? diffs)]
        (if any-drifted?
          {"state" "drifted"
           "diff"  (->> diffs
                        (filter :drifted?)
                        (map #(if (:message %)
                                (:message %)
                                (format-diff (:resource %) %)))
                        (str/join "\n\n"))}
          {"state" "converged"}))
      (catch Exception e
        {"state" "drifted"
         "diff"  (str "observe failed: " (.getMessage e))}))))

;;; ─── Dispatch ───────────────────────────────────────────────────────

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
