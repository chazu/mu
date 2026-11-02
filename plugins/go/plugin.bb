#!/usr/bin/env bb

;; Go toolchain plugin for mu.
;;
;; Treats `go build` as a single opaque action (Go manages its own compile/link
;; pipeline). It runs in mu's sandbox with the project's `go` scratch
;; toolchain and downloads modules itself when go.mod is present.
;;
;; Config options:
;;   output    - output binary name (default: last segment of target name)
;;   goos      - GOOS (default: host OS)
;;   goarch    - GOARCH (default: host arch)
;;   cgo       - enable CGO (default: false)
;;   tags      - build tags (array of strings)
;;   ldflags   - linker flags string
;;   gcflags   - compiler flags string
;;   trimpath  - strip file paths from binary (default: true)
;;   race      - enable race detector (default: false)
;;   buildmode - go build mode (default: "exe")
;;   pkg       - package path to build (default: ".")

(require '[cheshire.core :as json]
         '[clojure.string :as str])

(defn handle-discover []
  {"name"             "go"
   "version"          "0.2.0"
   "protocol_version" 1
   "description"      "Compile Go packages into executables or libraries"
   "consumes"         ["source:go" "go_module_cache" "native_library"]
   "produces"         ["executable" "go_archive" "c_archive" "c_shared"]
   "config_schema"    {"type"                 "object"
                       "properties"           {"output"    {"type" "string"  "description" "Output binary path"}
                                               "pkg"       {"type" "string"  "default" "." "description" "Package path to build"}
                                               "goos"      {"type" "string"  "description" "Target operating system (cross-compile)"}
                                               "goarch"    {"type" "string"  "description" "Target architecture (cross-compile)"}
                                               "cgo"       {"type" "boolean" "default" false "description" "Enable cgo"}
                                               "tags"      {"type" "array"   "items" {"type" "string"} "description" "Build tags to pass to the compiler"}
                                               "ldflags"   {"type" "string"  "description" "Linker flags (-ldflags)"}
                                               "gcflags"   {"type" "string"  "description" "Compiler flags (-gcflags)"}
                                               "trimpath"  {"type" "boolean" "default" true "description" "Remove file system paths from binary"}
                                               "race"      {"type" "boolean" "default" false "description" "Enable race detector"}
                                               "buildmode" {"type" "string"  "default" "exe" "description" "Go build mode (exe, c-archive, c-shared, etc.)"}}
                       "additionalProperties" false}})

(defn target-short-name
  "Extract short name from target like //cmd/server -> server"
  [target-name]
  (last (str/split target-name #"[:/]")))

;; Builds run in mu's sandbox with the project's `go` scratch toolchain: its
;; files are unpacked into the sandbox root, so bin/go is first on PATH and Go
;; derives GOROOT from it. HOME (and with it GOCACHE, GOPATH and GOMODCACHE)
;; is a fresh sandbox directory, so nothing from the host's Go setup leaks in.
(def go-toolchain "go")

(def passthrough-env
  "Host module-proxy settings, sent as env_passthrough (names only, never
   values) so private-module setups keep working without baking host values
   into the action key."
  ["GOPROXY" "GOPRIVATE" "GONOPROXY" "GONOSUMDB" "GOSUMDB" "GOINSECURE"])

(defn build-env
  "Build the declared (keyed) environment for go commands: only values
   chosen by the config, never copied from the planning machine.
   GOTOOLCHAIN=local keeps go from fetching another toolchain; -modcacherw
   leaves the module cache deletable when the sandbox is cleaned up."
  [config]
  (cond-> {"CGO_ENABLED" (if (get config "cgo" false) "1" "0")
           "GOTOOLCHAIN" "local"
           "GOFLAGS"     "-modcacherw"}
    (get config "goos")   (assoc "GOOS"   (get config "goos"))
    (get config "goarch") (assoc "GOARCH" (get config "goarch"))))

(defn build-command
  "Construct the go build command from config."
  [config output pkg]
  (let [cmd ["go" "build"]]
    (cond-> cmd
      (get config "trimpath" true)    (conj "-trimpath")
      (get config "race" false)       (conj "-race")
      (not= "exe" (get config "buildmode" "exe"))
                                      (conj (str "-buildmode=" (get config "buildmode")))
      (get config "tags")             (conj (str "-tags=" (str/join "," (get config "tags"))))
      (get config "ldflags")          (conj (str "-ldflags=" (get config "ldflags")))
      (get config "gcflags")          (conj (str "-gcflags=" (get config "gcflags")))
      true                            (conj (str "-o=" output))
      true                            (conj pkg))))

(defn find-go-mod
  "Find the go.mod source path. Returns nil if not found."
  [sources]
  (first (filter #(str/ends-with? % "go.mod") sources)))

(defn go-mod-dir
  "Extract the directory containing go.mod from its path.
   Returns nil if go.mod is at the root (no directory prefix)."
  [go-mod-path]
  (when go-mod-path
    (let [dir (str/replace go-mod-path #"/go\.mod$" "")]
      (when (and (not= dir go-mod-path) (not= dir ""))
        dir))))

(defn handle-plan [req]
  (let [target   (get req "target")
        tgt-name (get target "name")
        sources  (get target "sources" [])
        config   (get target "config" {})
        short    (target-short-name tgt-name)
        output   (get config "output" short)
        pkg      (get config "pkg" ".")
        env      (build-env config)
        cmd      (build-command config output pkg)

        ;; Detect go.mod and its directory for work_dir.
        go-mod   (find-go-mod sources)
        mod-dir  (go-mod-dir go-mod)
        has-mod  (some? go-mod)

        ;; Build input map: all declared sources
        inputs   (into {} (map (fn [s] [s s]) sources))

        ;; Action outputs are relative to work_dir; declared_outputs (what
        ;; downstream targets consume) are project-relative.
        output-path (if mod-dir (str mod-dir "/" output) output)

        ;; One action: the sandbox starts with an empty module cache, so the
        ;; build downloads its modules itself (network: true when go.mod is
        ;; present); a separate download action's cache would not survive
        ;; into the build's sandbox.
        actions
        [(cond-> {"id"              "build"
                  "command"         cmd
                  "inputs"          inputs
                  "outputs"         [output]
                  "depends_on"      []
                  "env"             env
                  "env_passthrough" passthrough-env
                  "toolchains"      [go-toolchain]
                  "network"         has-mod}
           mod-dir (assoc "work_dir" mod-dir))]]

    {"actions"          actions
     "declared_outputs" {"executable" output-path}}))

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
