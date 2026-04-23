package gateway

import (
	"os"
)

// PushTopicFor returns the Pulsar topic for push fan-out to a given gateway pod.
//
// Topic layout by environment (aligned with BACKEND.md §3.2):
//
//	prod  -> persistent://im/push/msg.push.{gatewayID}
//	pre   -> persistent://im/push-pre/msg.push.{gatewayID}
//	other -> persistent://im/push-local/msg.push.{gatewayID}.{dev-suffix}
//
// The "other" bucket is namespaced by $USER (falling back to $HOSTNAME, then
// the literal "anon") so developers sharing a Pulsar cluster never collide.
func PushTopicFor(gatewayID string, env string) string {
	switch env {
	case "prod":
		return "persistent://im/push/msg.push." + gatewayID
	case "pre":
		return "persistent://im/push-pre/msg.push." + gatewayID
	default:
		return "persistent://im/push-local/msg.push." + gatewayID + "." + devSuffix()
	}
}

// devSuffix picks a per-developer suffix for the local push topic namespace.
func devSuffix() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if h := os.Getenv("HOSTNAME"); h != "" {
		return h
	}
	return "anon"
}
