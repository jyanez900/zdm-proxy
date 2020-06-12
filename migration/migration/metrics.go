package migration

import (
	"encoding/json"
	"fmt"
	log "github.com/sirupsen/logrus"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Metrics contains migration metrics and information needed to derive these metrics
type Metrics struct {
	TablesMigrated int
	TablesLeft     int
	Speed          float64
	SizeMigrated   float64

	lock      *sync.Mutex
	port      int
	directory string
	s3        string
}

// NewMetrics creates a new Metrics instance based on the given s3 bucket and migration directory
func NewMetrics(port int, directory string, totalTables int, s3 string) *Metrics {
	metrics := Metrics{
		TablesLeft: totalTables,
		lock:       &sync.Mutex{},
		port:       port,
		directory:  directory,
		s3:         s3,
	}

	// Begin updating speed based on s3 bucket object size
	metrics.StartSpeedMetrics()

	return &metrics
}

// StartSpeedMetrics updates the speed and sizes of migration every second based on s3 bucket object size
func (m *Metrics) StartSpeedMetrics() {
	go func() {
		for {
			// Calculate size and derive speed of migration
			out, _ := exec.Command("aws", "s3", "ls", "--summarize", "--recursive", fmt.Sprintf("s3://%s/%s", m.s3, m.directory)).Output()
			r, _ := regexp.Compile("Total Size: [0-9]+")
			match := r.FindString(string(out))

			numBytes, _ := strconv.ParseFloat(match[12:], 64)

			// In MB/s and MB, respectively
			m.Speed = (numBytes / 1024 / 1024) - m.SizeMigrated
			m.SizeMigrated = numBytes / 1024 / 1024

			time.Sleep(time.Second)
		}
	}()
}

// Expose exposes the endpoint for metrics
func (m *Metrics) Expose() {
	go func() {
		http.HandleFunc("/", m.write)
		err := http.ListenAndServe(fmt.Sprintf(":%d", m.port), nil)
		log.WithError(err).Fatal("Metrics subservice failed.")
	}()
}

func (m *Metrics) write(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	marshaled, err := json.Marshal(m)
	if err != nil {
		w.Write([]byte(`{"error": "unable to grab metrics"}`))
		w.Write([]byte(err.Error()))
		return
	}
	w.Write(marshaled)
}

// IncrementTablesMigrated increments tables that have been migrated
func (m *Metrics) IncrementTablesMigrated() {
	m.lock.Lock()
	defer m.lock.Unlock()

	m.TablesMigrated++
}

// DecrementTablesMigrated decrements tables that have been migrated
func (m *Metrics) DecrementTablesMigrated() {
	m.lock.Lock()
	defer m.lock.Unlock()

	m.TablesMigrated--
}

// IncrementTablesLeft increments number of tables that need to be migrated
func (m *Metrics) IncrementTablesLeft() {
	m.lock.Lock()
	defer m.lock.Unlock()

	m.TablesLeft++
}

// DecrementTablesLeft decrements number of tables that need to be migrated
func (m *Metrics) DecrementTablesLeft() {
	m.lock.Lock()
	defer m.lock.Unlock()

	m.TablesLeft--
}
