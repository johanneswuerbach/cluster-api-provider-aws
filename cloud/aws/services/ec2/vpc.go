// Copyright © 2018 The Kubernetes Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ec2

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/golang/glog"
	"github.com/pkg/errors"
	"sigs.k8s.io/cluster-api-provider-aws/cloud/aws/providerconfig/v1alpha1"
)

const (
	defaultVpcCidr = "10.0.0.0/16"
)

func (s *Service) reconcileVPC(clusterName string, in *v1alpha1.VPC) error {
	glog.V(2).Infof("Reconciling VPC")

	vpc, err := s.describeVPC(clusterName, in.ID)
	if IsNotFound(err) {
		// Create a new vpc.
		vpc, err = s.createVPC(clusterName, in)
		if err != nil {
			return err
		}

	} else if err != nil {
		return err
	}

	vpc.DeepCopyInto(in)
	glog.V(2).Infof("Working on VPC %q", in.ID)
	return nil
}

func (s *Service) createVPC(clusterName string, v *v1alpha1.VPC) (*v1alpha1.VPC, error) {
	if v.CidrBlock == "" {
		v.CidrBlock = defaultVpcCidr
	}

	input := &ec2.CreateVpcInput{
		CidrBlock: aws.String(v.CidrBlock),
	}

	out, err := s.EC2.CreateVpc(input)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create vpc")
	}

	wReq := &ec2.DescribeVpcsInput{VpcIds: []*string{out.Vpc.VpcId}}
	if err := s.EC2.WaitUntilVpcAvailable(wReq); err != nil {
		return nil, errors.Wrapf(err, "failed to wait for vpc %q", *out.Vpc.VpcId)
	}

	if err := s.createTags(clusterName, *out.Vpc.VpcId, ResourceLifecycleOwned, nil); err != nil {
		return nil, errors.Wrapf(err, "failed to tag vpc %q", *out.Vpc.VpcId)
	}

	glog.V(2).Infof("Created new VPC %q with cidr %q", *out.Vpc.VpcId, *out.Vpc.CidrBlock)

	return &v1alpha1.VPC{
		ID:        *out.Vpc.VpcId,
		CidrBlock: *out.Vpc.CidrBlock,
	}, nil
}

func (s *Service) deleteVPC(v *v1alpha1.VPC) error {
	// TODO(johanneswuerbach): ensure that the VPC is owned by this cluster before deleting
	input := &ec2.DeleteVpcInput{
		VpcId: aws.String(v.ID),
	}

	_, err := s.EC2.DeleteVpc(input)
	if err != nil {
		return errors.Wrapf(err, "failed to delete vpc %q", v.ID)
	}

	glog.V(2).Infof("Deleted VPC %q", v.ID)
	return nil
}

func (s *Service) describeVPC(clusterName string, id string) (*v1alpha1.VPC, error) {
	input := &ec2.DescribeVpcsInput{}

	if id == "" {
		// Try to find a previously created and tagged VPC
		input.Filters = s.addTagFilters(clusterName, []*ec2.Filter{})
	} else {
		input.VpcIds = []*string{aws.String(id)}
	}

	out, err := s.EC2.DescribeVpcs(input)
	if err != nil {
		return nil, err
	}

	if len(out.Vpcs) == 0 {
		return nil, NewNotFound(errors.Errorf("could not find vpc %q", id))
	} else if len(out.Vpcs) > 1 {
		return nil, NewConflict(errors.Errorf("found more than one vpc with supplied filters. Please clean up extra VPCs: %s", out.GoString()))
	}

	return &v1alpha1.VPC{
		ID:        *out.Vpcs[0].VpcId,
		CidrBlock: *out.Vpcs[0].CidrBlock,
	}, nil
}
