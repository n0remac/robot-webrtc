package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/sashabaranov/go-openai"
)

const (
	contentDir     = "content"
	outputJSONFile = "processed_content.json"
)

// ContentWithKeywords holds the content and its generated keywords.
type ContentWithKeywords struct {
	Filename string   `json:"filename"`
	Content  string   `json:"content"`
	Keywords []string `json:"keywords"`
}

// KeywordsResponse represents the function call response for generating keywords.
type KeywordsResponse struct {
    Keywords string `json:"keywords"`
}

// generateKeywords extracts keywords from the provided text using the OpenAI API.
func generateKeywords(client *openai.Client, text string) ([]string, error) {
    // Define a system prompt that instructs the model to extract keywords from the text.
    systemPrompt := openai.ChatCompletionMessage{
        Role:    openai.ChatMessageRoleSystem,
        Content: "You are an assistant that extracts keywords from a given text. Your response should include a comma-separated list of keywords that directly appear in the text. Do not invent new keywords.",
    }

    // Build the user message with the provided text.
    userMessage := openai.ChatCompletionMessage{
        Role:    openai.ChatMessageRoleUser,
        Content: fmt.Sprintf("Extract keywords from the following text:\n\n%s", text),
    }

    // Define the function for generating keywords.
    fn := openai.FunctionDefinition{
        Name:        "generate_keywords",
        Description: "Generate a comma-separated list of keywords from a given text.",
        Parameters: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "keywords": map[string]any{
                    "type": "string",
                },
            },
            "required": []string{"keywords"},
        },
    }

    // Build the chat completion request.
    chatRequest := openai.ChatCompletionRequest{
        Model: "gpt-4-0613",
        Messages: []openai.ChatCompletionMessage{
            systemPrompt,
            userMessage,
        },
        Functions:    []openai.FunctionDefinition{fn},
        FunctionCall: openai.FunctionCall{Name: "generate_keywords"},
    }

    chatResp, err := client.CreateChatCompletion(context.Background(), chatRequest)
    if err != nil {
        return nil, fmt.Errorf("failed to get response from OpenAI: %w", err)
    }

    choice := chatResp.Choices[0]
    if choice.Message.FunctionCall == nil {
        return nil, fmt.Errorf("no function call in OpenAI response")
    }

    // Parse the function call arguments.
    var parsed KeywordsResponse
    err = json.Unmarshal([]byte(choice.Message.FunctionCall.Arguments), &parsed)
    if err != nil {
        return nil, fmt.Errorf("failed to unmarshal function response: %w", err)
    }

    // Split the comma-separated keywords and trim whitespace.
    parts := strings.Split(parsed.Keywords, ",")
    var keywords []string
    for _, p := range parts {
        trimmed := strings.TrimSpace(p)
        if trimmed != "" {
            keywords = append(keywords, trimmed)
        }
    }

    return keywords, nil
}


// processContent scans the contentDir, generates keywords if needed, and writes an aggregated JSON file.
func processContent() ([]ContentWithKeywords, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENAI_API_KEY not set")
	}
	client := openai.NewClient(apiKey)

	var aggregated []ContentWithKeywords

	// Walk the content directory.
	err := filepath.WalkDir(contentDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil // skip directories
		}
		if filepath.Ext(d.Name()) != ".txt" {
			return nil // only process .txt files
		}

		// Read the text file.
		data, err := ioutil.ReadFile(path)
		if err != nil {
			log.Printf("Error reading %s: %v", path, err)
			return nil
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			return nil
		}

		// Determine the expected JSON filename.
		base := strings.TrimSuffix(d.Name(), ".txt")
		jsonPath := filepath.Join(contentDir, base+".json")

		var keywords []string

		// Check if the JSON file exists.
		if _, err := os.Stat(jsonPath); err == nil {
			// JSON exists, load it.
			jdata, err := ioutil.ReadFile(jsonPath)
			if err != nil {
				log.Printf("Error reading JSON file %s: %v", jsonPath, err)
			} else {
				var stored ContentWithKeywords
				if err := json.Unmarshal(jdata, &stored); err != nil {
					log.Printf("Error unmarshaling JSON file %s: %v", jsonPath, err)
				} else {
					keywords = stored.Keywords
				}
			}
		} else {
			// JSON does not exist: generate keywords using OpenAI.
			generatedKeywords, err := generateKeywords(client, content)
			if err != nil {
				log.Printf("Error generating keywords for %s: %v", path, err)
			} else {
				keywords = generatedKeywords
				// Save the generated result to the JSON file.
				stored := ContentWithKeywords{
					Filename: d.Name(),
					Content:  content,
					Keywords: keywords,
				}
				jdata, err := json.MarshalIndent(stored, "", "  ")
				if err == nil {
					err := ioutil.WriteFile(jsonPath, jdata, 0644)
					if err != nil {
						log.Printf("Error writing JSON file %s: %v", jsonPath, err)
					}
				} else {
					log.Printf("Error marshaling JSON for %s: %v", path, err)
				}
			}
		}

		// Append the content record regardless (keywords may be empty if generation failed).
		aggregated = append(aggregated, ContentWithKeywords{
			Filename: d.Name(),
			Content:  content,
			Keywords: keywords,
		})

		return nil
	})
	if err != nil {
		return nil, err
	}
	// Write the aggregated results to the output file.
	outData, err := json.MarshalIndent(aggregated, "", "  ")
	if err != nil {
		return nil, err
	}
	err = ioutil.WriteFile(outputJSONFile, outData, 0644)
	if err != nil {
		return nil, err
	}
	return aggregated, nil
}
