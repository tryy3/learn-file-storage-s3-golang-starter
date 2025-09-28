package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

const maxMemory = 10 << 20

type Thumbnail struct {
	data      []byte
	mediaType string
}

var files map[uuid.UUID]Thumbnail = map[uuid.UUID]Thumbnail{}

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
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

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	if err = r.ParseMultipartForm(maxMemory); err != nil {
		respondWithError(w, http.StatusBadRequest, "Too large thumbnail", err)
		return
	}

	uploadedFile, fileHeader, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to find thumbnail in upload data", err)
		return
	}

	mediaType, _, err := mime.ParseMediaType(fileHeader.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unsupported file type", err)
		return
	}

	// fileContentType := fileHeader.Header["Content-Type"]
	// mediaType := fileContentType[0]

	// data, err := io.ReadAll(f)
	// if err != nil {
	// 	respondWithError(w, http.StatusBadRequest, "Unable to read the uploaded data", err)
	// 	return
	// }

	metadata, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to find video with ID", err)
		return
	}

	if metadata.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Not the owenr of the video", err)
		return
	}

	var fileExtension string
	switch mediaType {
	case "image/jpeg":
		fileExtension = "jpg"
	case "image/png":
		fileExtension = "png"
	default:
		respondWithError(w, http.StatusBadRequest, "Unsupported file type", err)
		return
	}

	randomNameBytes := make([]byte, 32)
	rand.Read(randomNameBytes)
	randomName := base64.RawURLEncoding.EncodeToString(randomNameBytes)

	fileName := fmt.Sprintf("%s.%s", randomName, fileExtension)
	filePath := filepath.Join(cfg.assetsRoot, fileName)

	if err = os.MkdirAll(cfg.assetsRoot, 0o755); err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to create assets folder", err)
		return
	}

	saveFile, err := os.Create(filePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to create file", err)
		return
	}

	if _, err = io.Copy(saveFile, uploadedFile); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to save file", err)
		return
	}

	url := fmt.Sprintf("http://localhost:8091/assets/%s", fileName)

	// t := thumbnail{
	// 	data:      data,
	// 	mediaType: mediaType,
	// }
	// videoThumbnails[videoID] = t
	//
	metadata.ThumbnailURL = &url
	cfg.db.UpdateVideo(metadata)

	respondWithJSON(w, http.StatusOK, metadata)
}
