// Command vault-plugin-secrets-bifrost is the plugin binary Vault runs over its
// plugin RPC. It serves the Bifrost secrets-engine backend defined in
// internal/bifrost.
package main

import (
	"os"

	"github.com/hashicorp/vault/api"
	"github.com/hashicorp/vault/sdk/plugin"

	"github.com/example/vault-plugin-secrets-bifrost/internal/bifrost"
)

func main() {
	apiClientMeta := &api.PluginAPIClientMeta{}
	flags := apiClientMeta.FlagSet()
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(1)
	}

	tlsConfig := apiClientMeta.GetTLSConfig()
	tlsProviderFunc := api.VaultPluginTLSProvider(tlsConfig)

	if err := plugin.ServeMultiplex(&plugin.ServeOpts{
		BackendFactoryFunc: bifrost.Factory,
		TLSProviderFunc:    tlsProviderFunc,
	}); err != nil {
		os.Exit(1)
	}
}
