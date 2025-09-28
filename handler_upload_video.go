package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

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

	s3Input := s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &fileName,
		Body:        tempFile,
		ContentType: &mediaType,
	}

	_, err = cfg.s3Client.PutObject(context.Background(), &s3Input)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error uploading to s3", err)
		return
	}
	videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, fileName)
	metadata.VideoURL = &videoURL
	if err = cfg.db.UpdateVideo(metadata); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error updating in DB", err)
		return
	}
}
