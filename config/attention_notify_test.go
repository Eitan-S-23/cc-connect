package config

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestAttentionNotifyConfigFlagsRoundTrip(t *testing.T) {
	for _, input := range []string{
		"",
		"[attention_notify]\nenabled = true\n",
		"[attention_notify]\nenabled = false\non_error = true\n",
		"[attention_notify]\nenabled = true\non_error = false\non_blocked = false\non_turn_complete = false\nmention_user = false\nmin_duration_secs = 30\n",
	} {
		t.Run(input, func(t *testing.T) {
			var original Config
			if _, err := toml.Decode(input, &original); err != nil {
				t.Fatal(err)
			}
			var encoded bytes.Buffer
			if err := toml.NewEncoder(&encoded).Encode(original); err != nil {
				t.Fatal(err)
			}
			var decoded Config
			if _, err := toml.Decode(encoded.String(), &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(original.AttentionNotify, decoded.AttentionNotify) {
				t.Fatalf("attention flags changed on config save: before=%+v after=%+v", original.AttentionNotify, decoded.AttentionNotify)
			}
		})
	}
}

func TestAttentionNotifyConfigDistinguishesDefaultFromOptOut(t *testing.T) {
	var defaults, optedOut Config
	if _, err := toml.Decode("[attention_notify]\nenabled = true", &defaults); err != nil {
		t.Fatal(err)
	}
	if _, err := toml.Decode("[attention_notify]\nenabled = true\non_error = false", &optedOut); err != nil {
		t.Fatal(err)
	}
	if defaults.AttentionNotify.OnError != nil {
		t.Fatal("omitted on_error must retain its default-on semantics")
	}
	if optedOut.AttentionNotify.OnError == nil || *optedOut.AttentionNotify.OnError {
		t.Fatal("explicit on_error=false was lost")
	}
}
