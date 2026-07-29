// Wire-format schemas for the AWS observer plugin.
//
// These schemas are intentionally plugin-owned. They describe the lowercase
// JSON records emitted by plugin.bb, including the account/region provenance
// attached by the observer. PUDL's semantic schemas live in PUDL.
package aws

#Tag: {
	Key:   string
	Value: string
	...
}

#Provenance: {
	account: string
	region:  string
}

#EC2Instance: {
	#Provenance
	instance_id:     string
	instance_type:   string
	state:           string
	vpc_id?:         string | null
	subnet_id?:      string | null
	private_ip?:     string | null
	public_ip?:      string | null
	image_id?:       string | null
	tags:            [...#Tag]
	security_groups: [...string]
	iam_profile?:    string | null
	...
}

#VPC: {
	#Provenance
	vpc_id:           string
	cidr_block:       string
	state:            string
	is_default:       bool
	tags:             [...#Tag]
	instance_tenancy: string | null
	...
}

#Subnet: {
	#Provenance
	subnet_id:                string
	vpc_id:                   string
	cidr_block:               string
	availability_zone:        string
	state:                    string
	map_public_ip_on_launch:  bool
	available_ip_count?:      int | null
	tags:                     [...#Tag]
	...
}
