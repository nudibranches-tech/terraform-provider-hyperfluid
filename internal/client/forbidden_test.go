// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import "testing"

func TestForbiddenMessage(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{
			// What the console actually sends when a read is denied.
			name: "permission and scope",
			body: `{"message":"Missing permission 'container_app:read' on 'default'","required_permission":"container_app:read"}`,
			want: `missing permission "container_app:read" in "default"; ask an organization administrator to grant it`,
		},
		{
			name: "org-wide denial carries no scope",
			body: `{"message":"Missing permission 'container_app:read'","required_permission":"container_app:read"}`,
			want: `missing permission "container_app:read"; ask an organization administrator to grant it`,
		},
		{
			// A route that predates the typed field: the key comes out of the message.
			name: "permission only in the message",
			body: `{"message":"Missing permission 'secret:read'"}`,
			want: `missing permission "secret:read"; ask an organization administrator to grant it`,
		},
		{
			// A governance decision rather than a missing grant: keep it verbatim.
			name: "not the permission shape",
			body: `{"message":"blocked by data policy"}`,
			want: `blocked by data policy`,
		},
		{
			name: "not JSON at all",
			body: `upstream refused`,
			want: `upstream refused`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := forbiddenMessage([]byte(c.body)); got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}
