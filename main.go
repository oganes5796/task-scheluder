package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/monitor"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/robfig/cron"
	"github.com/spf13/viper"

	_ "github.com/oganes5796/task-scheduler/docs"
)

type Task struct {
	ID       int               `json:"id"`
	Name     string            `json:"name"`
	Schedule string            `json:"schedule"`
	Type     string            `json:"type"`
	Params   map[string]string `json:"params"`
}

type Config struct {
	Database struct {
		Host     string
		Port     int
		User     string
		Password string
		Name     string
	}
	Server struct {
		Port string
	}
	Tasks []Task
}

var (
	config                 Config
	scheduler              *cron.Cron
	dbConn                 *pgx.Conn
	logger                 *slog.Logger
	taskExecutionCount     *prometheus.CounterVec
	taskExecutionDurations *prometheus.HistogramVec
)

func initLogger() {
	logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func loadConfig() {
	viper.SetConfigFile("config.yaml")
	if err := viper.ReadInConfig(); err != nil {
		logger.Error("Error reading config file", "error", err)
		os.Exit(1)
	}
	if err := viper.Unmarshal(&config); err != nil {
		logger.Error("Error unmarshal config", "error", err)
		os.Exit(1)
	}
}

func connectDB() {
	var err error
	ctx := context.Background()
	dsn := fmt.Sprintf("postgresql://%s:%s@%s:%d/%s",
		config.Database.User,
		config.Database.Password,
		config.Database.Host,
		config.Database.Port,
		config.Database.Name)
	dbConn, err = pgx.Connect(ctx, dsn)
	if err != nil {
		logger.Error("Unable to connect to database", "error", err)
	}
	logger.Info("Connected to the database succefully.")
}

func closeDBConect() {
	if dbConn != nil {
		dbConn.Close(context.Background())
		logger.Info("Database connection closed.")
	}
}

func setupMetrics() {
	taskExecutionCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "task_execution_count",
			Help: "Number of times a task was executed.",
		},
		[]string{"type"},
	)
	taskExecutionDurations = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "task_execution_durations",
			Help:    "Duration of task execution.",
			Buckets: []float64{0.001, 0.01, 0.1, 1, 10, 60},
		},
		[]string{"type"},
	)

	prometheus.MustRegister(taskExecutionCount, taskExecutionDurations)
}

func createTasksTable() {
	ctx := context.Background()
	query := `CREATE TABLE IF NOT EXISTS tasks (
		id SERIAL PRIMARY KEY,
		name TEXT NOT NULL,
		schedule TEXT NOT NULL,
		type TEXT NOT NULL,
		params JSONB NOT NULL
	)`
	_, err := dbConn.Exec(ctx, query)
	if err != nil {
		logger.Error("Error creating tasks table,", "error", err)
	}
	logger.Info("Task table ensured.")
}

func getTasks(ctx context.Context) ([]Task, error) {
	rows, err := dbConn.Query(ctx, "SELECT * FROM tasks")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks := config.Tasks
	for rows.Next() {
		var task Task
		var paramsJSON []byte
		if err := rows.Scan(&task.ID, &task.Name, &task.Schedule, &task.Type, &paramsJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(paramsJSON, &task.Params); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func addTask(ctx context.Context, task Task) error {
	paramsJSON, err := json.Marshal(task.Params)
	if err != nil {
		return err
	}
	query := "INSERT INTO tasks (name, schedule, type, params) VALUES ($1, $2, $3, $4)"
	_, err = dbConn.Exec(ctx, query, task.Name, task.Schedule, task.Type, paramsJSON)
	return err
}

func executeHTTPTask(task Task) {
	url, ok := task.Params["Url"]
	if !ok {
		logger.Info("Task",
			"task_name", task.Name,
			"url", url,
		)
		return
	}
	resp, err := http.Get(url)
	if err != nil {
		logger.Info("Error executing task",
			"task_name", task.Name,
			"error", err,
		)
		return
	}
	defer resp.Body.Close()
	logger.Info("Task executed, HTTP response status:",
		"task_name", task.Name,
		"resp_status", resp.Status,
	)
}

func executeEmailTask(task Task) {
	recipient, ok := task.Params["Recipient"]
	if !ok {
		logger.Info("Task",
			"task_name", task.Name,
			"recipient", recipient,
		)
		return
	}
	subject, ok := task.Params["Subject"]
	if !ok {
		logger.Info("Task",
			"task_name", task.Name,
			"subject", subject,
		)
		return
	}
	body, ok := task.Params["Body"]
	if !ok {
		logger.Info("Task",
			"task_name", task.Name,
			"body", body,
		)
		return
	}
	logger.Info("Sending email",
		"recipient", recipient,
		"subject", subject,
		"body", body,
	)
}

func executeCommandTask(task Task) {
	command, ok := task.Params["Command"]
	if !ok {
		logger.Info("Task",
			"task_name", task.Name,
			"command", command,
		)
		return
	}
	cmd := exec.Command("/bin/sh", "-c", command)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		logger.Info("Error executing task",
			"task_name", task.Name,
			"error", err,
		)
		return
	}
	logger.Info("Task",
		"task_name", task.Name,
		"out", out.String())
}

func executeTask(task Task) {
	start := time.Now()

	switch task.Type {
	case "http":
		executeHTTPTask(task)
	case "email":
		executeEmailTask(task)
	case "command":
		executeCommandTask(task)
	default:
		logger.Info("Unknown task type for task", "task_name", task.Name)
	}

	duration := time.Since(start).Seconds()
	taskExecutionCount.WithLabelValues(task.Type).Inc()
	taskExecutionDurations.WithLabelValues(task.Type).Observe(duration)
}

func addTasksToScheduler(tasks []Task) {
	for _, task := range tasks {
		t := task
		if err := scheduler.AddFunc(task.Schedule, func() { executeTask(t) }); err != nil {
			logger.Error("Error adding task to schedule",
				"task_name", task.Name,
				"error", err,
			)
		}
	}
}

func gracefulShutdown(app *fiber.App) {

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	<-stop
	logger.Info("Shutting down gracefully...")
	scheduler.Stop()
	if err := app.Shutdown(); err != nil {
		logger.Error("error shutting down server", "error", err)
	}

	closeDBConect()
}

func getTasksHandler(c *fiber.Ctx) error {
	tasks, err := getTasks(c.Context())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}
	return c.JSON(tasks)
}

func createTasksHandler(c *fiber.Ctx) error {
	var task Task
	if err := c.BodyParser(&task); err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}
	if err := addTask(c.Context(), task); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}
	if err := scheduler.AddFunc(task.Schedule, func() { executeTask(task) }); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}
	return c.SendStatus(fiber.StatusCreated)
}

func updatedTaskHandler(c *fiber.Ctx) error {
	id := c.Params("id")
	var updatedTask Task
	if err := c.BodyParser(&updatedTask); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":   "Invalid request body",
			"details": err.Error(),
		})
	}

	// обновляем задачу в бд
	ctx := context.Background()
	query := "UPDATE tasks SET name=$1, schedule=$2, type=$3, params=$4 WHERE id=$5"
	_, err := dbConn.Exec(ctx, query, updatedTask.Name, updatedTask.Schedule, updatedTask.Type, updatedTask.Params, id)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":   "Error updating task",
			"details": err.Error(),
		})
	}

	// перепланировка задачи
	scheduler.Stop()
	tasks, _ := getTasks(ctx)
	scheduler = cron.New()
	for _, task := range tasks {
		scheduler.AddFunc(task.Schedule, func() { executeTask(task) })
	}
	scheduler.Start()

	return c.JSON(fiber.Map{"message": "Task updated", "id": id})
}

func deletedTaskHandler(c *fiber.Ctx) error {
	id := c.Params("id")

	// удаление задачи из бд
	ctx := context.Background()
	query := "DELETE FROM tasks WHERE id=$1"
	_, err := dbConn.Exec(ctx, query, id)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":   "Error deleting task",
			"details": err.Error(),
		})
	}

	// перепланировка задачи
	scheduler.Stop()
	tasks, _ := getTasks(ctx)
	scheduler = cron.New()
	for _, task := range tasks {
		scheduler.AddFunc(task.Schedule, func() { executeTask(task) })
	}
	scheduler.Start()

	return c.JSON(fiber.Map{"message": "Task deleted", "id": id})
}

// @title Task Scheduler API
// @version 1.0
// @description REST API для управления задачами в Task Scheduler.
// @contact.name Ваше Имя
// @contact.url https://github.com/ваш-профиль
// @contact.email ваша@почта.com

// @host localhost:8080
// @BasePath /
func main() {
	// инициализация логера
	initLogger()
	logger.Info("Starting Task Scheduler...")

	// загрзка конфигурационного файла
	loadConfig()

	// подключение к БД
	connectDB()

	// подключение метрики
	setupMetrics()

	// создание таблицы
	createTasksTable()

	// создание экземпляра планировщика
	scheduler = cron.New()
	// запуск планировщика
	scheduler.Start()

	// загружаем задачи из базы данных
	tasks, err := getTasks(context.Background())
	if err != nil {
		logger.Error("Error loading tasks:", "error", err)
		os.Exit(1)
	}

	// добавление задачи в планировщик
	addTasksToScheduler(tasks)

	// запуск Fiber
	app := fiber.New()

	app.Get("/tasks", getTasksHandler)
	app.Post("/tasks", createTasksHandler)
	app.Put("/tasks/:id", updatedTaskHandler)
	app.Delete("/tasks/:id", deletedTaskHandler)

	// Добавляем маршрут для метрик
	app.Get("/metrics", monitor.New(monitor.Config{Title: "Task Scheduler Metrics"}))

	// Грейсфул-шатдаун
	go func() {
		if err := app.Listen(":" + config.Server.Port); err != nil {
			logger.Error("Error stariting server", "error", err)
			os.Exit(1)
		}
	}()

	gracefulShutdown(app)
}
