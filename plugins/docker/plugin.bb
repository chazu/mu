#!/usr/bin/env bb

;; Docker build plugin for mu.
;;
;; Builds Docker/OCI images using `docker build`. Supports Dockerfile path,
;; build args, target stage, platform, and tagging.
;;
;; Config options:
;;   dockerfile - path to Dockerfile (default: "Dockerfile")
;;   tag        - image tag (default: target short name)
;;   context    - build context directory (default: ".")
;;   target     - multi-stage build target
;;   platform   - target platform (e.g. "linux/amd64")
;;   build_args - map of build arguments
;;   no_cache   - disable Docker layer cache (default: false)
;;   push       - push image after build (default: false)
;;   labels     - map of image labels

(require '[cheshire.core :as json]
         '[clojure.string :as str])

(defn handle-discover []
  {"name"             "docker"
   "version"          "0.1.0"
   "protocol_version" 1
   "consumes"         ["source:dockerfile" "source:any"]
   "produces"         ["docker_image"]
   "config_schema"    {"dockerfile" {"type" "string"  "default" "Dockerfile"}
                       "tag"        {"type" "string"}
                       "context"    {"type" "string"  "default" "."}
                       "target"     {"type" "string"}
                       "platform"   {"type" "string"}
                       "build_args" {"type" "object"}
                       "no_cache"   {"type" "boolean" "default" false}
                       "push"       {"type" "boolean" "default" false}
                       "labels"     {"type" "object"}}})

(defn target-short-name [target-name]
  (last (str/split target-name #"[:/]")))

(defn build-docker-command
  "Construct the docker build command from config."
  [config tag context-dir]
  (let [dockerfile (get config "dockerfile" "Dockerfile")
        cmd        ["docker" "build"]]
    (cond-> cmd
      true                     (conj "-f" dockerfile)
      true                     (conj "-t" tag)
      (get config "target")    (conj "--target" (get config "target"))
      (get config "platform")  (conj "--platform" (get config "platform"))
      (get config "no_cache")  (conj "--no-cache")

      ;; Build args
      (get config "build_args")
      (into (mapcat (fn [[k v]] ["--build-arg" (str k "=" v)])
                    (get config "build_args")))

      ;; Labels
      (get config "labels")
      (into (mapcat (fn [[k v]] ["--label" (str k "=" v)])
                    (get config "labels")))

      ;; Context dir is always last
      true (conj context-dir))))

(defn handle-plan [req]
  (let [target      (get req "target")
        tgt-name    (get target "name")
        sources     (get target "sources" [])
        config      (get target "config" {})
        short       (target-short-name tgt-name)
        tag         (get config "tag" short)
        context-dir (get config "context" ".")
        push?       (get config "push" false)

        ;; Build input map from declared sources
        inputs (into {} (map (fn [s] [s s]) sources))

        ;; Docker build command
        build-cmd (build-docker-command config tag context-dir)

        ;; Actions
        actions
        (cond-> [{"id"         "docker-build"
                  "command"    build-cmd
                  "inputs"     inputs
                  "outputs"    []
                  "depends_on" []
                  "env"        {}
                  "network"    true}]

          ;; Optionally push after build
          push?
          (conj {"id"         "docker-push"
                 "command"    ["docker" "push" tag]
                 "inputs"     {}
                 "outputs"    []
                 "depends_on" ["docker-build"]
                 "env"        {}
                 "network"    true}))]

    {"actions"          actions
     "declared_outputs" {"docker_image" tag}}))

(defn handle-request [req]
  (case (get req "method")
    "discover" (handle-discover)
    "plan"     (handle-plan req)
    {"error" (str "unknown method: " (get req "method"))}))

;; Main NDJSON loop
(loop []
  (when-let [line (read-line)]
    (let [req  (json/parse-string line)
          resp (handle-request req)]
      (println (json/generate-string resp))
      (flush)
      (recur))))
