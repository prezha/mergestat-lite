package cmd

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/mergestat/mergestat/pkg/display"
	"github.com/spf13/cobra"
)

var (
	servicePort int
)

type ServiceQueryRequest struct {
	Query string `json:"query"`
}

type queryServiceHandler struct {
	DB *sql.DB
}

func (h *queryServiceHandler) init() error {
	if db, err := sql.Open("sqlite3", ":memory:"); err != nil {
		return fmt.Errorf("failed to initialize database connection: %v", err)
	} else {
		h.DB = db
	}
	return nil
}

func (h *queryServiceHandler) handleErr(w http.ResponseWriter, statusCode int, err error) {
	if statusCode == 0 {
		statusCode = http.StatusInternalServerError
	}
	w.WriteHeader(statusCode)

	var output []byte
	var marshalErr error
	if output, marshalErr = json.Marshal(map[string]string{
		"error": err.Error(),
	}); marshalErr != nil {
		logger.Error().Msg(marshalErr.Error())
		return
	}

	if _, err := w.Write(output); err != nil {
		logger.Error().Msg(err.Error())
		return
	}

	logger.Warn().Msgf("handled request with error code=%d, message=%s", statusCode, err.Error())
}

func (h *queryServiceHandler) httpHandler(w http.ResponseWriter, req *http.Request) {
	var body []byte
	var err error
	if body, err = ioutil.ReadAll(req.Body); err != nil {
		h.handleErr(w, http.StatusBadRequest, err)
		return
	}

	var serviceQueryRequest ServiceQueryRequest
	if err = json.Unmarshal(body, &serviceQueryRequest); err != nil {
		h.handleErr(w, http.StatusBadRequest, err)
		return
	}

	if rows, err := h.DB.QueryContext(req.Context(), serviceQueryRequest.Query); err != nil {
		h.handleErr(w, http.StatusInternalServerError, err)
		return
	} else {
		if err = display.WriteTo(rows, w, "json", false); err != nil {
			h.handleErr(w, http.StatusInternalServerError, err)
			return
		}
	}

	logger.Info().Msgf(`handled request for query=%q`, serviceQueryRequest.Query)
}

func init() {
	serveCmd.Flags().IntVarP(&servicePort, "port", "p", 8000, "port to listen on")
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run an HTTP API server for receiving queries to execute",
	Long:  `Use this command to start a query API server`,
	Args:  cobra.ExactArgs(0),
	Run: func(cmd *cobra.Command, args []string) {

		srv := &queryServiceHandler{}
		if err := srv.init(); err != nil {
			handleExitError(err)
		}
		defer srv.DB.Close()

		http.HandleFunc("/", srv.httpHandler)
		http.HandleFunc("/query", srv.httpHandler)

		logger.Info().Msgf("starting HTTP API server on port %d", servicePort)
		if err := http.ListenAndServe(fmt.Sprintf(":%d", servicePort), nil); err != nil {
			handleExitError(err)
		}
	},
}
