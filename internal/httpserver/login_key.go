package httpserver

import "strings"

// loginKeyPlan is what a GitHub login does to the account's sk-gt keys.
//   - first login, or a project with no active "cli" key: the "cli" key the store minted is returned (issued);
//   - rotate (explicit `llms login --rotate-key`): every active "cli" key is revoked and one new "cli" key is minted;
//   - a device that has no local key (`device_key`): a key of its own is minted, named after the device, and no
//     other key is touched — each device authorization gets its own credential, as in the OAuth device flow
//     (RFC 8628; GitHub issues a new token per device authorization), and keys stay revocable one by one;
//   - otherwise the device keeps the key it already has.
type loginKeyPlan struct {
	revokeCLI bool
	mintName  string // "" = mint nothing
}

func planLoginKey(created, issued, rotate, deviceKey bool, deviceName string) loginKeyPlan {
	switch {
	case created:
		return loginKeyPlan{}
	case rotate:
		return loginKeyPlan{revokeCLI: true, mintName: "cli"}
	case issued:
		return loginKeyPlan{} // the store's new "cli" key already serves this device
	case deviceKey:
		return loginKeyPlan{mintName: deviceKeyName(deviceName)}
	}
	return loginKeyPlan{}
}

// maxDeviceNameLen is the longest DNS host name (RFC 1035 §2.3.4: 255 octets on the wire, 253 as text); the CLI
// sends os.Hostname().
const maxDeviceNameLen = 253

// deviceKeyName is "cli:<host>", so `llms key list` shows which device holds which key. The host name comes from
// the client: only host-name characters are kept (RFC 1123 §2.1 letters, digits, '-', plus '.' and '_' seen in
// local names), and an empty or too long result falls back to "cli:device".
func deviceKeyName(host string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(host) {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_' {
			b.WriteRune(r)
		}
	}
	name := b.String()
	if name == "" || len(name) > maxDeviceNameLen {
		name = "device"
	}
	return "cli:" + name
}
