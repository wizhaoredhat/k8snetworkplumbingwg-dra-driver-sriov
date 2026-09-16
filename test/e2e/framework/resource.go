package framework

import (
	resourceapi "k8s.io/api/resource/v1"
)

// deviceAttributeString returns the string value of a ResourceSlice device attribute, if set and non-empty.
func deviceAttributeString(attrs map[resourceapi.QualifiedName]resourceapi.DeviceAttribute, key resourceapi.QualifiedName) (string, bool) {
	attr, ok := attrs[key]
	if !ok || attr.StringValue == nil || *attr.StringValue == "" {
		return "", false
	}
	return *attr.StringValue, true
}
