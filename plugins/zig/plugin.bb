#!/usr/bin/env bb

;; Zig toolchain plugin for mu.
;;
;; Builds Zig projects using `zig build` or compiles individual files with
;; `zig build-exe`/`zig build-lib`. Supports cross-compilation via Zig's
;; built-in target triple support.
;;
;; Config options:
;;   output     - output binary/library name (default: target short name)
;;   mode       - build mode: "build" (zig build) or "compile" (zig build-exe/lib)
;;                (default: "build")
;;   target     - cross-compile target triple (e.g. "x86_64-linux-gnu")
;;   optimize   - optimization: "Debug", "ReleaseSafe", "ReleaseFast", "ReleaseSmall"
;;                (default: "Debug")
;;   step       - zig build step to run (default: "install", only for mode "build")
;;   build_file - path to build.zig (default: "build.zig")
;;   emit       - what to emit: "exe", "lib", "obj" (default: "exe", only for mode "compile")
;;   flags      - extra flags passed to zig (array of strings)
;;   pkg        - package path for compile mode (default: first source file)

(require '[cheshire.core :as json]
         '[clojure.string :as str])

(defn handle-discover []
  {"name"             "zig"
   "version"          "0.1.0"
   "protocol_version" 1
   "description"      "Compile Zig projects via build system or direct compilation"
   "consumes"         ["source:zig"]
   "produces"         ["executable" "zig_library" "object_file"]
   "config_schema"    {"output"     {"type" "string" "description" "Output artifact path"}
                       "mode"       {"type" "string"  "default" "build"  "enum" ["build" "compile"] "description" "Build mode: use build.zig system or compile a single file"}
                       "target"     {"type" "string" "description" "Cross-compilation target triple"}
                       "optimize"   {"type" "string"  "default" "Debug"
                                     "enum" ["Debug" "ReleaseSafe" "ReleaseFast" "ReleaseSmall"]
                                     "description" "Optimization level"}
                       "step"       {"type" "string"  "default" "install" "description" "Build system step to run"}
                       "build_file" {"type" "string"  "default" "build.zig" "description" "Path to build.zig"}
                       "emit"       {"type" "string"  "default" "exe" "enum" ["exe" "lib" "obj"] "description" "Artifact type to produce"}
                       "flags"      {"type" "array"   "items" "string" "description" "Additional flags passed to the compiler"}
                       "pkg"        {"type" "string" "description" "Package path for direct compilation"}}})

(defn target-short-name [target-name]
  (last (str/split target-name #"[:/]")))

(defn find-build-zig
  "Find the build.zig source path. Returns nil if not found."
  [sources]
  (first (filter #(str/ends-with? % "build.zig") sources)))

(defn build-zig-dir
  "Extract the directory containing build.zig from its path."
  [build-zig-path]
  (when build-zig-path
    (let [dir (str/replace build-zig-path #"/build\.zig$" "")]
      (when (and (not= dir build-zig-path) (not= dir ""))
        dir))))

(defn build-mode-command
  "Construct a `zig build` command."
  [config step]
  (let [cmd ["zig" "build"]]
    (cond-> cmd
      (not= step "install") (conj step)
      (get config "target")
      (conj (str "-Dtarget=" (get config "target")))
      (not= "Debug" (get config "optimize" "Debug"))
      (conj (str "-Doptimize=" (get config "optimize")))
      (get config "flags")
      (into (get config "flags")))))

(defn compile-mode-command
  "Construct a `zig build-exe` or `zig build-lib` command."
  [config output pkg emit]
  (let [subcmd (case emit
                 "lib" "build-lib"
                 "obj" "build-obj"
                 "build-exe")
        cmd    ["zig" subcmd]]
    (cond-> cmd
      true (conj pkg)
      true (conj (str "-femit-binary=" output))
      (get config "target")
      (conj (str "-target=" (get config "target")))
      (not= "Debug" (get config "optimize" "Debug"))
      (conj (str "-O" (get config "optimize")))
      (get config "flags")
      (into (get config "flags")))))

(defn handle-plan [req]
  (let [target   (get req "target")
        tgt-name (get target "name")
        sources  (get target "sources" [])
        config   (get target "config" {})
        short    (target-short-name tgt-name)
        output   (get config "output" short)
        mode     (get config "mode" "build")
        emit     (get config "emit" "exe")
        step     (get config "step" "install")

        ;; Detect build.zig and its directory for work_dir
        build-zig     (find-build-zig sources)
        zig-dir       (build-zig-dir build-zig)

        ;; Build input map
        inputs (into {} (map (fn [s] [s s]) sources))

        ;; Construct the command based on mode
        cmd (if (= mode "compile")
              (compile-mode-command config output
                                   (get config "pkg" (first sources))
                                   emit)
              (build-mode-command config step))

        ;; Output path
        output-path (if (= mode "build")
                      ;; zig build puts outputs in zig-out/bin/ by default
                      (if zig-dir
                        (str zig-dir "/zig-out/bin/" output)
                        (str "zig-out/bin/" output))
                      ;; compile mode: output is where we say
                      (if zig-dir (str zig-dir "/" output) output))

        ;; Build action
        action (cond-> {"id"         "build"
                        "command"    cmd
                        "inputs"     inputs
                        "outputs"    [output-path]
                        "depends_on" []
                        "env"        {}
                        "network"    false}
                 zig-dir (assoc "work_dir" zig-dir))

        ;; Determine output type
        output-type (case emit
                      "lib" "zig_library"
                      "obj" "object_file"
                      "executable")]

    {"actions"          [action]
     "declared_outputs" {output-type output-path}}))

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
