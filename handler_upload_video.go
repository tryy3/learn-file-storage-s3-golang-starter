package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

type ffprobeOutputStruct struct {
	Streams []struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"streams"`
}

func getVideoAspectRatio(filepath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filepath)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("error running ffprobe: %w", err)
	}

	var outputData ffprobeOutputStruct
	if err := json.Unmarshal(out.Bytes(), &outputData); err != nil {
		return "", fmt.Errorf("error decoding ffprobe output: %w", err)
	}

	width := outputData.Streams[0].Width
	height := outputData.Streams[0].Height

	// Calculate the actual aspect ratio as a float
	actualRatio := float64(width) / float64(height)

	// Find the simplest fraction that represents this ratio within tolerance
	const tolerance = 0.05    // 5% tolerance
	const maxDenominator = 20 // Check denominators up to 20

	bestNumerator := width
	bestDenominator := height
	bestError := 1.0

	// Try to find the simplest (smallest denominator) aspect ratio within tolerance
	for denominator := 1; denominator <= maxDenominator; denominator++ {
		// Find the closest numerator for this denominator
		numerator := int(actualRatio*float64(denominator) + 0.5) // Round to nearest int

		if numerator == 0 {
			continue
		}

		// Calculate the ratio this fraction represents
		testRatio := float64(numerator) / float64(denominator)

		// Calculate relative error
		relativeError := (testRatio - actualRatio) / actualRatio
		if relativeError < 0 {
			relativeError = -relativeError
		}

		// If within tolerance and simpler (smaller denominator) than current best
		if relativeError < tolerance && relativeError < bestError {
			bestNumerator = numerator
			bestDenominator = denominator
			bestError = relativeError
		}
	}

	return fmt.Sprintf("%d:%d", bestNumerator, bestDenominator), nil
}

func processVideoForFastStart(filePath string) (string, error) {
	newPath := fmt.Sprintf("%s.processing", filePath)
	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", newPath)

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("error processing video: %w", err)
	}

	return newPath, nil
}

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	http.MaxBytesReader(w, r.Body, 1<<30)

	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	metadata, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to find video with ID", err)
		return
	}

	if metadata.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Not the owenr of the video", err)
		return
	}

	videoFile, fileHeader, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to read uploaded video", err)
		return
	}
	defer videoFile.Close()

	mediaType, _, err := mime.ParseMediaType(fileHeader.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unsupported file type", err)
		return
	}

	var fileExtension string
	switch mediaType {
	case "video/mp4":
		fileExtension = "mp4"
	default:
		respondWithError(w, http.StatusBadRequest, "Unsupported file type", err)
		return
	}

	randomNameBytes := make([]byte, 32)
	rand.Read(randomNameBytes)
	randomName := base64.RawURLEncoding.EncodeToString(randomNameBytes)

	fileName := fmt.Sprintf("%s.%s", randomName, fileExtension)

	tempFile, err := os.CreateTemp("", fmt.Sprintf("tubely-video.%s", fileExtension))
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error trying to save temp file", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	if _, err = io.Copy(tempFile, videoFile); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to save file", err)
		return
	}
	tempFile.Seek(0, io.SeekStart)

	aspectRatio, err := getVideoAspectRatio(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to get aspect ratio of video", err)
		return
	}

	var orientation string
	switch aspectRatio {
	case "16:9":
		orientation = "landscape"
	case "9:16":
		orientation = "portrait"
	default:
		orientation = "other"
	}
	fmt.Printf("Uploaded: %s - %s", aspectRatio, orientation)

	processedFilePath, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error processing video for fast start", err)
		return
	}
	processedFile, err := os.Open(processedFilePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error opening processed video file", err)
		return
	}
	defer os.Remove(processedFilePath)
	defer processedFile.Close()

	bucketKey := fmt.Sprintf("%s/%s", orientation, fileName)

	s3Input := s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &bucketKey,
		Body:        processedFile,
		ContentType: &mediaType,
	}

	_, err = cfg.s3Client.PutObject(context.Background(), &s3Input)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error uploading to s3", err)
		return
	}
	videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, bucketKey)
	metadata.VideoURL = &videoURL
	if err = cfg.db.UpdateVideo(metadata); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error updating in DB", err)
		return
	}
}
