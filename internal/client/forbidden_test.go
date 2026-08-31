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
			// A quota refusal is also a 403. Its message already carries the numbers,
			// and the spec declares none of the fields they also arrive in, so it has
			// to survive verbatim.
			name: "quota refusal keeps its numbers",
			body: `{"message":"Quota exceeded for container_apps: using 3/3. Please upgrade your plan.","code":"QUOTA_EXCEEDED","resource_type":"container_apps","current":3,"limit":3}`,
			want: `Quota exceeded for container_apps: using 3/3. Please upgrade your plan.`,
		},
		{
			// An apostrophe must not be mistaken for a quoted permission key.
			name: "message with an apostrophe",
			body: `{"message":"the organization's plan does not include this feature"}`,
			want: `the organization's plan does not include this feature`,
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
