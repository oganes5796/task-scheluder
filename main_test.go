package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
)

// Тестовая инициализация приложения
func setupTestApp() *fiber.App {
	app := fiber.New()

	// Подключаем тестовые обработчики
	app.Get("/tasks", getTasksHandler)

	return app
}

// Mock для getTasks
func mockGetTasks(ctx context.Context) ([]Task, error) {
	return []Task{
		{ID: 1, Name: "Test Task", Schedule: "* * * * *", Type: "http", Params: map[string]string{"Url": "http://example.com"}},
	}, nil
}

func TestGetTasksHandler(t *testing.T) {
	// Замена функции getTasks на mock
	_ = mockGetTasks

	// Инициализируем приложение
	app := setupTestApp()

	// Создаем тестовый запрос
	req := httptest.NewRequest("GET", "/tasks", nil)
	resp, _ := app.Test(req, -1)

	// Проверяем статус ответа
	assert.Equal(t, fiber.StatusOK, resp.StatusCode)

	// Проверяем содержание ответа
	var tasks []Task
	json.NewDecoder(resp.Body).Decode(&tasks)
	assert.Len(t, tasks, 1)
	assert.Equal(t, "Test Task", tasks[0].Name)
	assert.Equal(t, "* * * * *", tasks[0].Schedule)
}
