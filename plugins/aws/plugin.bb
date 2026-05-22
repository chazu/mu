#!/usr/bin/env bb

;; AWS observation plugin for mu.
;;
;; Capabilities: discover, observe.
;;
;; Observes AWS resource state via the AWS CLI. V1 supports EC2 instances,
;; VPCs, and subnets. Returns structured JSON records for pudl to import.
;;
;; Config options:
;;   profile   - AWS CLI profile name (required)
;;   region    - AWS region (required)
;;   resources - list of resource types to observe (required)
;;
;; Valid resource types: "ec2", "vpc", "subnet"

(require '[cheshire.core :as json]
         '[babashka.process :as process]
         '[clojure.string :as str])

;;; ─── Constants ───────────────────────────────────────────────────────

(def valid-resources #{"ec2" "vpc" "subnet"})

(def resource-schemas
  {"ec2"    "aws.ec2.instance"
   "vpc"    "aws.ec2.vpc"
   "subnet" "aws.ec2.subnet"})

;;; ─── AWS CLI helpers ─────────────────────────────────────────────────

(defn check-aws-cli!
  "Verify AWS CLI v2 is installed. Returns nil on success, throws on failure."
  []
  (let [result (process/sh ["aws" "--version"])]
    (when-not (zero? (:exit result))
      (throw (ex-info "AWS CLI not found. Install AWS CLI v2: https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html" {})))
    (when-not (str/starts-with? (str/trim (:out result)) "aws-cli/2.")
      (throw (ex-info (str "AWS CLI v2 required, found: " (str/trim (:out result))
                           ". Upgrade: https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html") {})))))

(defn aws-cmd
  "Build an AWS CLI command vector with --profile and --region."
  [profile region & args]
  (into ["aws" "--profile" profile "--region" region "--output" "json"] args))

(defn run-aws!
  "Run an AWS CLI command, return parsed JSON. Throws on failure."
  [cmd]
  (let [result (process/sh cmd)]
    (if (zero? (:exit result))
      (when-not (str/blank? (:out result))
        (json/parse-string (:out result)))
      (throw (ex-info (str "AWS CLI failed: " (str/trim (:err result))) {})))))

;;; ─── STS identity (cached) ──────────────────────────────────────────

(def sts-cache (atom {}))

(defn get-account-id
  "Get AWS account ID via STS. Caches per profile+region."
  [profile region]
  (let [cache-key (str profile ":" region)]
    (if-let [cached (get @sts-cache cache-key)]
      cached
      (let [result (run-aws! (aws-cmd profile region "sts" "get-caller-identity"))
            account (get result "Account")]
        (swap! sts-cache assoc cache-key account)
        account))))

;;; ─── Config validation ──────────────────────────────────────────────

(defn validate-config
  "Validate target config. Returns nil on success, error string on failure."
  [config]
  (let [profile   (get config "profile")
        region    (get config "region")
        resources (get config "resources")]
    (cond
      (str/blank? profile)
      "aws plugin requires \"profile\" in config"

      (str/blank? region)
      "aws plugin requires \"region\" in config"

      (or (nil? resources) (empty? resources))
      "aws plugin requires non-empty \"resources\" list in config"

      :else
      (let [unknown (remove valid-resources resources)]
        (when (seq unknown)
          (str "unknown resource type(s): " (str/join ", " unknown)
               "; valid types: " (str/join ", " (sort valid-resources))))))))

;;; ─── Resource gatherers ─────────────────────────────────────────────

(defn attach-identity
  "Attach _schema, account, and region to each record.
   Designed for ->> threading: records is the last parameter."
  [schema account region records]
  (mapv (fn [r] (assoc r "_schema" schema "account" account "region" region))
        records))

(defn gather-ec2
  "Observe EC2 instances."
  [profile region account]
  (let [raw (run-aws! (aws-cmd profile region
                               "ec2" "describe-instances"
                               "--query" "Reservations[].Instances[]"))
        instances (or raw [])]
    (->> instances
         (mapv (fn [inst]
                 {"instance_id"     (get inst "InstanceId")
                  "instance_type"   (get inst "InstanceType")
                  "state"           (get-in inst ["State" "Name"])
                  "vpc_id"          (get inst "VpcId")
                  "subnet_id"       (get inst "SubnetId")
                  "private_ip"      (get inst "PrivateIpAddress")
                  "public_ip"       (get inst "PublicIpAddress")
                  "image_id"        (get inst "ImageId")
                  "tags"            (or (get inst "Tags") [])
                  "security_groups" (mapv #(get % "GroupId")
                                         (or (get inst "SecurityGroups") []))
                  "iam_profile"     (get-in inst ["IamInstanceProfile" "Arn"])}))
         (attach-identity (resource-schemas "ec2") account region))))

(defn gather-vpc
  "Observe VPCs."
  [profile region account]
  (let [raw (run-aws! (aws-cmd profile region
                               "ec2" "describe-vpcs"
                               "--query" "Vpcs[]"))
        vpcs (or raw [])]
    (->> vpcs
         (mapv (fn [vpc]
                 {"vpc_id"           (get vpc "VpcId")
                  "cidr_block"       (get vpc "CidrBlock")
                  "state"            (get vpc "State")
                  "is_default"       (get vpc "IsDefault")
                  "tags"             (or (get vpc "Tags") [])
                  "instance_tenancy" (get vpc "InstanceTenancy")}))
         (attach-identity (resource-schemas "vpc") account region))))

(defn gather-subnet
  "Observe subnets."
  [profile region account]
  (let [raw (run-aws! (aws-cmd profile region
                               "ec2" "describe-subnets"
                               "--query" "Subnets[]"))
        subnets (or raw [])]
    (->> subnets
         (mapv (fn [sub]
                 {"subnet_id"              (get sub "SubnetId")
                  "vpc_id"                 (get sub "VpcId")
                  "cidr_block"             (get sub "CidrBlock")
                  "availability_zone"      (get sub "AvailabilityZone")
                  "state"                  (get sub "State")
                  "map_public_ip_on_launch" (get sub "MapPublicIpOnLaunch")
                  "available_ip_count"     (get sub "AvailableIpAddressCount")
                  "tags"                   (or (get sub "Tags") [])}))
         (attach-identity (resource-schemas "subnet") account region))))

;;; ─── Dispatch map ───────────────────────────────────────────────────

(def gatherers
  {"ec2"    gather-ec2
   "vpc"    gather-vpc
   "subnet" gather-subnet})

;;; ─── Discover ───────────────────────────────────────────────────────

(defn handle-discover []
  (try
    (check-aws-cli!)
    {"name"             "aws"
     "version"          "0.1.0"
     "protocol_version" 1
     "description"      "Observe AWS resources via the CLI and emit state records"
     "consumes"         []
     "produces"         ["aws_state"]
     "capabilities"     ["discover" "observe"]
     "config_schema"    {"profile"   {"type" "string" "description" "AWS CLI named profile"}
                         "region"    {"type" "string" "description" "AWS region to query"}
                         "resources" {"type" "array"  "description" "Resource types to gather (e.g. ec2, s3, iam-role)"}}}
    (catch Exception e
      {"error" (.getMessage e)})))

;;; ─── Observe ────────────────────────────────────────────────────────

(defn handle-observe [req]
  (let [target  (get req "target")
        config  (get target "config" {})]
    (if-let [err (validate-config config)]
      {"error" err}
      (try
        (let [profile   (get config "profile")
              region    (get config "region")
              resources (get config "resources")
              account   (get-account-id profile region)
              records   (into [] (mapcat (fn [rtype]
                                          ((get gatherers rtype) profile region account))
                                        resources))]
          {"current" {"records" records}})
        (catch Exception e
          {"error" (str "observe failed: " (.getMessage e))})))))

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
