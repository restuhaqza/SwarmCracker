package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePublishSpec_Valid(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []PublishedPort
	}{
		{
			name: "tcp default",
			in:   []string{"8080:80"},
			want: []PublishedPort{{Protocol: "tcp", TargetPort: 80, PublishedPort: 8080}},
		},
		{
			name: "explicit tcp",
			in:   []string{"8080:80/tcp"},
			want: []PublishedPort{{Protocol: "tcp", TargetPort: 80, PublishedPort: 8080}},
		},
		{
			name: "udp",
			in:   []string{"53:53/udp"},
			want: []PublishedPort{{Protocol: "udp", TargetPort: 53, PublishedPort: 53}},
		},
		{
			name: "multiple",
			in:   []string{"8080:80", "8443:443", "53:53/udp"},
			want: []PublishedPort{
				{Protocol: "tcp", TargetPort: 80, PublishedPort: 8080},
				{Protocol: "tcp", TargetPort: 443, PublishedPort: 8443},
				{Protocol: "udp", TargetPort: 53, PublishedPort: 53},
			},
		},
		{
			name: "uppercase proto",
			in:   []string{"8080:80/UDP"},
			want: []PublishedPort{{Protocol: "udp", TargetPort: 80, PublishedPort: 8080}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePublishSpec(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParsePublishSpec_Invalid(t *testing.T) {
	tests := []struct {
		name string
		in   []string
	}{
		{"empty", []string{""}},
		{"host missing", []string{"80"}},
		{"host port zero", []string{"0:80"}},
		{"container port zero", []string{"8080:0"}},
		{"port too large", []string{"8080:70000"}},
		{"non numeric", []string{"http:80"}},
		{"bad protocol", []string{"8080:80/sctp"}},
		{"too many colons", []string{"8080:80:90"}},
		{"duplicate host port", []string{"8080:80", "8080:81"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePublishSpec(tt.in)
			assert.Error(t, err)
		})
	}
}

func TestPublishLabel_RoundTrip(t *testing.T) {
	ports := []PublishedPort{
		{Protocol: "tcp", TargetPort: 80, PublishedPort: 8080},
		{Protocol: "udp", TargetPort: 53, PublishedPort: 5353},
	}
	label := FormatPublishLabel(ports)
	assert.Equal(t, "8080:80/tcp,5353:53/udp", label)

	got, err := ParsePublishLabel(label)
	require.NoError(t, err)
	assert.Equal(t, ports, got)
}

func TestParsePublishLabel_Empty(t *testing.T) {
	got, err := ParsePublishLabel("")
	require.NoError(t, err)
	assert.Nil(t, got)

	got, err = ParsePublishLabel("   ")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestPublishedPort_String(t *testing.T) {
	p := PublishedPort{Protocol: "tcp", TargetPort: 80, PublishedPort: 8080}
	assert.Equal(t, "8080:80/tcp", p.String())
}
