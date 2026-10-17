// reflect_client.go —— gRPC ServerReflectionInfo 简版 client（list/listMethods/
// describeSymbol——reflection 全 dump 证据面，补「名单 grep」那层缺口）。
package main

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	rpb "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/protobuf/types/descriptorpb"
)

// findService 服务描述符定位。
func findService(f *descriptorpb.FileDescriptorProto, name string) *descriptorpb.ServiceDescriptorProto {
	for i := 0; i < len(f.GetService()); i++ {
		if svc := f.GetService()[i]; fullProtoName(f.GetPackage(), svc.GetName()) == name {
			return svc
		}
	}
	return nil
}

// findMessage 消息描述符定位（顶层+一层嵌套）。
func findMessage(f *descriptorpb.FileDescriptorProto, name string) *descriptorpb.DescriptorProto {
	for i := 0; i < len(f.GetMessageType()); i++ {
		m := f.GetMessageType()[i]
		if fullProtoName(f.GetPackage(), m.GetName()) == name {
			return m
		}
		for j := 0; j < len(m.GetNestedType()); j++ {
			if nested := m.GetNestedType()[j]; fullProtoName(f.GetPackage(), m.GetName()+"."+nested.GetName()) == name {
				return nested
			}
		}
	}
	return nil
}

func fullProtoName(pkg, name string) string {
	if pkg == "" {
		return name
	}
	return pkg + "." + name
}

// methodNameToInput 方法→输入消息全名（proto 默认 input 命名=方法名同名消息）。
func methodNameToInput(service, method string) string {
	// mtapi 命名=Request 与方法同名（Account→AccountRequest）。
	return strings.TrimSuffix(method, "Reply") + "Request"
}

// mt5ReflectionClient reflection bidi 流简版封装。
type mt5ReflectionClient struct {
	conn *grpc.ClientConn
}

func (r mt5ReflectionClient) stream(ctx context.Context) (rpb.ServerReflection_ServerReflectionInfoClient, error) {
	c := rpb.NewServerReflectionClient(r.conn)
	return c.ServerReflectionInfo(ctx)
}

// listServices 服务清单。
func (r mt5ReflectionClient) listServices(ctx context.Context) ([]string, error) {
	sc, err := r.stream(ctx)
	if err != nil {
		return nil, err
	}
	if err := sc.Send(&rpb.ServerReflectionRequest{MessageRequest: &rpb.ServerReflectionRequest_ListServices{ListServices: "*"}}); err != nil {
		return nil, err
	}
	resp, err := sc.Recv()
	if err != nil {
		return nil, err
	}
	l := resp.GetListServicesResponse()
	var out []string
	for _, s := range l.GetService() {
		out = append(out, s.GetName())
	}
	return out, nil
}

// listMethods 服务下方法清单（FileContainingSymbol→FileDescriptorProto 解析）。
func (r mt5ReflectionClient) listMethods(ctx context.Context, service string) ([]string, error) {
	fdps, err := r.fileBySymbol(ctx, service)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range fdps {
		svc := findService(f, service)
		if svc == nil {
			continue
		}
		for i := 0; i < len(svc.GetMethod()); i++ {
			out = append(out, svc.GetMethod()[i].GetName())
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("service %s not found in descriptors", service)
	}
	return out, nil
}

// describeSymbol 消息字段清单+嵌套枚举值（字段名:类型 形态）。
func (r mt5ReflectionClient) describeSymbol(ctx context.Context, symbol string) (fields []string, enums map[string][]string, err error) {
	enums = map[string][]string{}
	fdps, err := r.fileBySymbol(ctx, symbol)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range fdps {
		msg := findMessage(f, symbol)
		if msg == nil {
			continue
		}
		for i := 0; i < len(msg.GetField()); i++ {
			fld := msg.GetField()[i]
			fields = append(fields, fmt.Sprintf("%s:%d", fld.GetName(), fld.GetNumber()))
		}
		for i := 0; i < len(msg.GetEnumType()); i++ {
			en := msg.GetEnumType()[i]
			full := fmt.Sprintf("%s.%s", symbol, en.GetName())
			var vals []string
			for _, ev := range en.GetValue() {
				vals = append(vals, fmt.Sprintf("%s=%d", ev.GetName(), ev.GetNumber()))
			}
			enums[full] = vals
		}
	}
	return fields, enums, nil
}

// fileBySymbol FileContainingSymbol（transitive deps 递归收集）。
func (r mt5ReflectionClient) fileBySymbol(ctx context.Context, symbol string) ([]*fdpProto, error) {
	sc, err := r.stream(ctx)
	if err != nil {
		return nil, err
	}
	if err := sc.Send(&rpb.ServerReflectionRequest{MessageRequest: &rpb.ServerReflectionRequest_FileContainingSymbol{FileContainingSymbol: symbol}}); err != nil {
		return nil, err
	}
	resp, err := sc.Recv()
	if err != nil {
		return nil, err
	}
	var out []*fdpProto
	for _, b := range resp.GetFileDescriptorResponse().GetFileDescriptorProto() {
		var fdp fdpProto
		if err := proto.Unmarshal(b, &fdp); err != nil {
			continue
		}
		out = append(out, &fdp)
	}
	return out, nil
}

// fdpProto descriptorpb 别名（find helpers 用）。
type fdpProto = descriptorpb.FileDescriptorProto
