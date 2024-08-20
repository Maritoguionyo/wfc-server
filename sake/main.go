package sake

import (
	"context"
	"fmt"
	"net/http"
	"wwfc/common"
	"wwfc/logging"

	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/logrusorgru/aurora/v3"
)

var (
	ctx  = context.Background()
	pool *pgxpool.Pool
)

func StartServer(reload bool) {
	// Get config
	config := common.GetConfig()

	common.ReadGameList()

	// Start SQL
	dbString := fmt.Sprintf("postgres://%s:%s@%s/%s", config.Username, config.Password, config.DatabaseAddress, config.DatabaseName)
	dbConf, err := pgxpool.ParseConfig(dbString)
	if err != nil {
		panic(err)
	}

	pool, err = pgxpool.ConnectConfig(ctx, dbConf)
	if err != nil {
		panic(err)
	}
}

func Shutdown() {
}

func HandleRequest(w http.ResponseWriter, r *http.Request) {
	logging.Info("SAKE", aurora.Yellow(r.Method), aurora.Cyan(r.URL), "via", aurora.Cyan(r.Host), "from", aurora.BrightCyan(r.RemoteAddr))

	switch r.URL.String() {
	case "/SakeStorageServer/StorageServer.asmx":
		moduleName := "SAKE:Storage:" + r.RemoteAddr
		handleStorageRequest(moduleName, w, r)
	}
}
