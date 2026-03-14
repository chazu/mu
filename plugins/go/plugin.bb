#!/usr/bin/env bb

;; Go toolchain plugin for mu.
;;
;; Treats `go build` as a single opaque action (Go manages its own compile/link
;; pipeline). A separate `go mod download` action handles network-dependent
;; module fetching when go.mod is present.
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
   "version"          "0.1.0"
   "protocol_version" 1
   "consumes"         ["source:go" "go_module_cache" "native_library"]
   "produces"         ["executable" "go_archive" "c_archive" "c_shared"]
   "config_schema"    {"output"    {"type" "string"}
                       "pkg"       {"type" "string"  "default" "."}
                       "goos"      {"type" "string"}
                       "goarch"    {"type" "string"}
                       "cgo"       {"type" "boolean" "default" false}
                       "tags"      {"type" "array"   "items" "string"}
                       "ldflags"   {"type" "string"}
                       "gcflags"   {"type" "string"}
                       "trimpath"  {"type" "boolean" "default" true}
                       "race"      {"type" "boolean" "default" false}
                       "buildmode" {"type" "string"  "default" "exe"}}})

(defn target-short-name
  "Extract short name from target like //cmd/server -> server"
  [target-name]
  (last (str/split target-name #"[:/]")))

(defn build-env
  "Build the environment map for go commands.
   Sets GOCACHE and GOPATH explicitly since mu's executor runs with a
   hermetic (stripped) environment — HOME is not available."
  [config]
  (let [home    (System/getProperty "user.home")
        gocache (or (System/getenv "GOCACHE")
                    (str home "/.cache/go-build"))
        gopath  (or (System/getenv "GOPATH")
                    (str home "/go"))
        gomod   (or (System/getenv "GOMODCACHE")
                    (str gopath "/pkg/mod"))
        path    (System/getenv "PATH")]
    (cond-> {"CGO_ENABLED" (if (get config "cgo" false) "1" "0")
             "GOCACHE"     gocache
             "GOPATH"      gopath
             "GOMODCACHE"  gomod
             "HOME"        home
             "PATH"        path}
      (get config "goos")   (assoc "GOOS"   (get config "goos"))
      (get config "goarch") (assoc "GOARCH" (get config "goarch")))))

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

(defn has-go-mod?
  "Check if go.mod is among the sources or infer it should exist."
  [sources]
  (some #(= % "go.mod") sources))

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

        ;; Build input map: all declared sources
        inputs   (into {} (map (fn [s] [s s]) sources))

        ;; Always include go.mod and go.sum if they exist in sources
        ;; (the user should list them, but we make sure they're keyed correctly)
        has-mod  (has-go-mod? sources)

        ;; Actions list — conditionally include mod-download
        actions
        (cond-> []
          ;; If go.mod is in sources, add a mod-download action first
          has-mod
          (conj {"id"         "mod-download"
                 "command"    ["go" "mod" "download"]
                 "inputs"     (select-keys inputs ["go.mod" "go.sum"])
                 "outputs"    []
                 "depends_on" []
                 "env"        (assoc env "GOFLAGS" "-modcacherw")
                 "network"    true})

          ;; Always add the build action
          true
          (conj {"id"         "build"
                 "command"    cmd
                 "inputs"     inputs
                 "outputs"    [output]
                 "depends_on" (if has-mod ["mod-download"] [])
                 "env"        env
                 "network"    false}))]

    {"actions"          actions
     "declared_outputs" {"executable" output}}))

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
