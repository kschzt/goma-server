// Copyright 2018 The Goma Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.28.1
// 	protoc        v3.21.5
// source: auth/authdb.proto

package auth

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type CheckMembershipReq struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Email string `protobuf:"bytes,1,opt,name=email,proto3" json:"email,omitempty"`
	Group string `protobuf:"bytes,2,opt,name=group,proto3" json:"group,omitempty"`
}

func (x *CheckMembershipReq) Reset() {
	*x = CheckMembershipReq{}
	if protoimpl.UnsafeEnabled {
		mi := &file_auth_authdb_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *CheckMembershipReq) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CheckMembershipReq) ProtoMessage() {}

func (x *CheckMembershipReq) ProtoReflect() protoreflect.Message {
	mi := &file_auth_authdb_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CheckMembershipReq.ProtoReflect.Descriptor instead.
func (*CheckMembershipReq) Descriptor() ([]byte, []int) {
	return file_auth_authdb_proto_rawDescGZIP(), []int{0}
}

func (x *CheckMembershipReq) GetEmail() string {
	if x != nil {
		return x.Email
	}
	return ""
}

func (x *CheckMembershipReq) GetGroup() string {
	if x != nil {
		return x.Group
	}
	return ""
}

type CheckMembershipResp struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	IsMember bool `protobuf:"varint,1,opt,name=is_member,json=isMember,proto3" json:"is_member,omitempty"`
}

func (x *CheckMembershipResp) Reset() {
	*x = CheckMembershipResp{}
	if protoimpl.UnsafeEnabled {
		mi := &file_auth_authdb_proto_msgTypes[1]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *CheckMembershipResp) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CheckMembershipResp) ProtoMessage() {}

func (x *CheckMembershipResp) ProtoReflect() protoreflect.Message {
	mi := &file_auth_authdb_proto_msgTypes[1]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CheckMembershipResp.ProtoReflect.Descriptor instead.
func (*CheckMembershipResp) Descriptor() ([]byte, []int) {
	return file_auth_authdb_proto_rawDescGZIP(), []int{1}
}

func (x *CheckMembershipResp) GetIsMember() bool {
	if x != nil {
		return x.IsMember
	}
	return false
}

var File_auth_authdb_proto protoreflect.FileDescriptor

var file_auth_authdb_proto_rawDesc = []byte{
	0x0a, 0x11, 0x61, 0x75, 0x74, 0x68, 0x2f, 0x61, 0x75, 0x74, 0x68, 0x64, 0x62, 0x2e, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x12, 0x04, 0x61, 0x75, 0x74, 0x68, 0x22, 0x40, 0x0a, 0x12, 0x43, 0x68, 0x65,
	0x63, 0x6b, 0x4d, 0x65, 0x6d, 0x62, 0x65, 0x72, 0x73, 0x68, 0x69, 0x70, 0x52, 0x65, 0x71, 0x12,
	0x14, 0x0a, 0x05, 0x65, 0x6d, 0x61, 0x69, 0x6c, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x05,
	0x65, 0x6d, 0x61, 0x69, 0x6c, 0x12, 0x14, 0x0a, 0x05, 0x67, 0x72, 0x6f, 0x75, 0x70, 0x18, 0x02,
	0x20, 0x01, 0x28, 0x09, 0x52, 0x05, 0x67, 0x72, 0x6f, 0x75, 0x70, 0x22, 0x32, 0x0a, 0x13, 0x43,
	0x68, 0x65, 0x63, 0x6b, 0x4d, 0x65, 0x6d, 0x62, 0x65, 0x72, 0x73, 0x68, 0x69, 0x70, 0x52, 0x65,
	0x73, 0x70, 0x12, 0x1b, 0x0a, 0x09, 0x69, 0x73, 0x5f, 0x6d, 0x65, 0x6d, 0x62, 0x65, 0x72, 0x18,
	0x01, 0x20, 0x01, 0x28, 0x08, 0x52, 0x08, 0x69, 0x73, 0x4d, 0x65, 0x6d, 0x62, 0x65, 0x72, 0x42,
	0x28, 0x5a, 0x26, 0x67, 0x6f, 0x2e, 0x63, 0x68, 0x72, 0x6f, 0x6d, 0x69, 0x75, 0x6d, 0x2e, 0x6f,
	0x72, 0x67, 0x2f, 0x67, 0x6f, 0x6d, 0x61, 0x2f, 0x73, 0x65, 0x72, 0x76, 0x65, 0x72, 0x2f, 0x70,
	0x72, 0x6f, 0x74, 0x6f, 0x2f, 0x61, 0x75, 0x74, 0x68, 0x62, 0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f,
	0x33,
}

var (
	file_auth_authdb_proto_rawDescOnce sync.Once
	file_auth_authdb_proto_rawDescData = file_auth_authdb_proto_rawDesc
)

func file_auth_authdb_proto_rawDescGZIP() []byte {
	file_auth_authdb_proto_rawDescOnce.Do(func() {
		file_auth_authdb_proto_rawDescData = protoimpl.X.CompressGZIP(file_auth_authdb_proto_rawDescData)
	})
	return file_auth_authdb_proto_rawDescData
}

var file_auth_authdb_proto_msgTypes = make([]protoimpl.MessageInfo, 2)
var file_auth_authdb_proto_goTypes = []interface{}{
	(*CheckMembershipReq)(nil),  // 0: auth.CheckMembershipReq
	(*CheckMembershipResp)(nil), // 1: auth.CheckMembershipResp
}
var file_auth_authdb_proto_depIdxs = []int32{
	0, // [0:0] is the sub-list for method output_type
	0, // [0:0] is the sub-list for method input_type
	0, // [0:0] is the sub-list for extension type_name
	0, // [0:0] is the sub-list for extension extendee
	0, // [0:0] is the sub-list for field type_name
}

func init() { file_auth_authdb_proto_init() }
func file_auth_authdb_proto_init() {
	if File_auth_authdb_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_auth_authdb_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*CheckMembershipReq); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_auth_authdb_proto_msgTypes[1].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*CheckMembershipResp); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_auth_authdb_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   2,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_auth_authdb_proto_goTypes,
		DependencyIndexes: file_auth_authdb_proto_depIdxs,
		MessageInfos:      file_auth_authdb_proto_msgTypes,
	}.Build()
	File_auth_authdb_proto = out.File
	file_auth_authdb_proto_rawDesc = nil
	file_auth_authdb_proto_goTypes = nil
	file_auth_authdb_proto_depIdxs = nil
}
