// PUDL semantic mappings for the records emitted by this plugin.
//
// The mu/aws schemas describe the plugin's wire format. These mappings point
// that format at PUDL-owned semantic schemas; they are classification metadata,
// not a claim that the two CUE shapes are identical.
package pudl

#Mapping: {
	resource_type: string
	schema:        string
}

mappings: [
	{
		resource_type: "aws.ec2.instance"
		schema:        "pudl/aws.#Instance"
	},
	{
		resource_type: "aws.ec2.vpc"
		schema:        "pudl/aws.#VPC"
	},
	{
		resource_type: "aws.ec2.subnet"
		schema:        "pudl/aws.#Subnet"
	},
]
