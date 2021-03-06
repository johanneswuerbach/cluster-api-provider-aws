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

func (s *Service) reconcileRouteTables(in *v1alpha1.Network) error {
	glog.V(2).Infof("Reconciling routing tables")

	subnetRouteMap, err := s.describeVpcRouteTablesBySubnet(in.VPC.ID)
	if err != nil {
		return err
	}

	for _, sn := range in.Subnets {
		if igw, ok := subnetRouteMap[sn.ID]; ok {
			glog.V(2).Infof("Subnet %q is already associated with route table %q", sn.ID, *igw.RouteTableId)
			// TODO(vincepri): if the route table ids are both non-empty and they don't match, replace the association.
			// TODO(vincepri): check that everything is in order, e.g. routes match the subnet type.
			continue
		}

		// For each subnet that doesn't have a routing table associated with it,
		// create a new table with the appropriate default routes and associate it to the subnet.
		var routes []*ec2.Route
		if sn.IsPublic {
			if in.InternetGatewayID == nil {
				return errors.Errorf("failed to create routing tables: internet gateway for %q is nil", in.VPC.ID)
			}

			routes = s.getDefaultPublicRoutes(*in.InternetGatewayID)
		} else {
			natGatewayId, err := s.getNatGatewayForSubnet(in.Subnets, sn)
			if err != nil {
				return err
			}

			routes = s.getDefaultPrivateRoutes(natGatewayId)
		}

		rt, err := s.createRouteTableWithRoutes(&in.VPC, routes)
		if err != nil {
			return err
		}

		if err := s.associateRouteTable(rt, sn.ID); err != nil {
			return err
		}

		glog.V(2).Infof("Subnet %q has been associated with route table %q", sn.ID, rt.ID)
		sn.RouteTableID = aws.String(rt.ID)
	}

	return nil
}

func (s *Service) describeVpcRouteTablesBySubnet(vpcID string) (map[string]*ec2.RouteTable, error) {
	rts, err := s.describeVpcRouteTables(vpcID)
	if err != nil {
		return nil, err
	}

	// Amazon allows a subnet to be associated only with a single routing table
	// https://docs.aws.amazon.com/vpc/latest/userguide/VPC_Route_Tables.html.
	res := make(map[string]*ec2.RouteTable)
	for _, rt := range rts {
		for _, as := range rt.Associations {
			if as.SubnetId == nil {
				continue
			}

			res[*as.SubnetId] = rt
		}
	}

	return res, nil
}

func (s *Service) describeVpcRouteTables(vpcID string) ([]*ec2.RouteTable, error) {
	out, err := s.EC2.DescribeRouteTables(&ec2.DescribeRouteTablesInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []*string{aws.String(vpcID)},
			},
		},
	})

	if err != nil {
		return nil, errors.Wrapf(err, "failed to describe route tables in vpc %q", vpcID)
	}

	return out.RouteTables, nil
}

func (s *Service) createRouteTableWithRoutes(vpc *v1alpha1.VPC, routes []*ec2.Route) (*v1alpha1.RouteTable, error) {
	out, err := s.EC2.CreateRouteTable(&ec2.CreateRouteTableInput{
		VpcId: aws.String(vpc.ID),
	})

	if err != nil {
		return nil, errors.Wrapf(err, "failed to create route table in vpc %q", vpc.ID)
	}

	for _, route := range routes {
		_, err := s.EC2.CreateRoute(&ec2.CreateRouteInput{
			RouteTableId:                out.RouteTable.RouteTableId,
			DestinationCidrBlock:        route.DestinationCidrBlock,
			DestinationIpv6CidrBlock:    route.DestinationIpv6CidrBlock,
			EgressOnlyInternetGatewayId: route.EgressOnlyInternetGatewayId,
			GatewayId:                   route.GatewayId,
			InstanceId:                  route.InstanceId,
			NatGatewayId:                route.NatGatewayId,
			NetworkInterfaceId:          route.NetworkInterfaceId,
			VpcPeeringConnectionId:      route.VpcPeeringConnectionId,
		})

		if err != nil {
			// TODO(vincepri): cleanup the route table if this fails.
			return nil, errors.Wrapf(err, "failed to create route in route table %q: %s", *out.RouteTable.RouteTableId, route.GoString())
		}
	}

	return &v1alpha1.RouteTable{
		ID: *out.RouteTable.RouteTableId,
	}, nil
}

func (s *Service) associateRouteTable(rt *v1alpha1.RouteTable, subnetID string) error {
	_, err := s.EC2.AssociateRouteTable(&ec2.AssociateRouteTableInput{
		RouteTableId: aws.String(rt.ID),
		SubnetId:     aws.String(subnetID),
	})

	if err != nil {
		return errors.Wrapf(err, "failed to associate route table %q to subnet %q", rt.ID, subnetID)
	}

	return nil
}

func (s *Service) getDefaultPrivateRoutes(natGatewayId string) []*ec2.Route {
	return []*ec2.Route{
		{
			DestinationCidrBlock: aws.String("0.0.0.0/0"),
			NatGatewayId:         aws.String(natGatewayId),
		},
	}
}

func (s *Service) getDefaultPublicRoutes(internetGatewayId string) []*ec2.Route {
	return []*ec2.Route{
		{
			DestinationCidrBlock: aws.String("0.0.0.0/0"),
			GatewayId:            aws.String(internetGatewayId),
		},
	}
}
