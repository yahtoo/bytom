package commands

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"
	jww "github.com/spf13/jwalterweatherman"
)

var accessTokenCreatCmd = &cobra.Command{
	Use:   "create-access-token",
	Short: "Create a access token",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) != 1 {
			jww.ERROR.Println("create-access-token needs 1 args")
			return
		}

		type Token struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		var token Token
		token.ID = args[0]

		var response interface{}

		client := mustRPCClient()
		client.Call(context.Background(), "/create-access-token", &token, &response)

		jww.FEEDBACK.Printf("response: %v\n", response)
	},
}

var accessTokenListCmd = &cobra.Command{
	Use:   "list-access-token",
	Short: "list access tokens",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) != 0 {
			jww.ERROR.Println("list-access-token needs 0 args")
			return
		}

		type Token struct {
			ID      string    `json:"id"`
			Token   string    `json:"token,omitempty"`
			Type    string    `json:"type,omitempty"`
			Created time.Time `json:"created_at"`
		}
		var response interface{}

		client := mustRPCClient()
		client.Call(context.Background(), "/list-access-token", nil, &response)
		jww.FEEDBACK.Printf("%v\n", response)
	},
}

var accessTokenDeleteCmd = &cobra.Command{
	Use:   "delete-access-token",
	Short: "delete a access token",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) != 1 {
			jww.ERROR.Println("delete-access-token needs 1 args")
			return
		}

		type Token struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		var token Token
		token.ID = args[0]

		var response interface{}

		client := mustRPCClient()
		client.Call(context.Background(), "/delete-access-token", &token, &response)

		jww.FEEDBACK.Printf("response: %v\n", response)
	},
}

var accessTokenCheckCmd = &cobra.Command{
	Use:   "check-access-token",
	Short: "check a access token",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) != 1 {
			jww.ERROR.Println("check-access-token needs 1 args")
			return
		}

		type Token struct {
			ID     string `json:"id"`
			Secret string `json:"secret,omitempty"`
		}
		var token Token
		inputs := strings.Split(args[0], ":")
		token.ID = inputs[0]
		token.Secret = inputs[1]
		var response interface{}
		client := mustRPCClient()
		client.Call(context.Background(), "/check-access-token", &token, &response)

		jww.FEEDBACK.Printf("response: %v\n", response)
	},
}
