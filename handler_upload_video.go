package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

// Max upload size: 1GB
const maxUploadSize = 1 << 30

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	// Limit request body size
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	// Extract videoID from URL
	videoID := r.PathValue("videoID")
	videoUUID, err := uuid.Parse(videoID)
	if err != nil {
		http.Error(w, "Invalid video ID", http.StatusBadRequest)
		return
	}

	// Authenticate user
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

	// Fetch video metadata from DB
	video, err := cfg.db.GetVideo(videoUUID)
	if err != nil || video.UserID != userID {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse uploaded file
	file, _, err := r.FormFile("video")   //use file, header if logging required later
	if err != nil {
		http.Error(w, "Invalid file upload", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Validate file MIME type
	buf := make([]byte, 512)
	if _, err := file.Read(buf); err != nil {
		http.Error(w, "Failed to read file", http.StatusBadRequest)
		return
	}
	file.Seek(0, io.SeekStart) // Reset read pointer

	mimeType := http.DetectContentType(buf)
	if mimeType != "video/mp4" {
		http.Error(w, "Invalid file type, must be MP4", http.StatusBadRequest)
		return
	}

	// Save to temporary file
	tempFile, err := os.CreateTemp("", "tubely-upload-*.mp4")
	if err != nil {
		http.Error(w, "Failed to create temp file", http.StatusInternalServerError)
		return
	}
	defer os.Remove(tempFile.Name()) // Cleanup
	defer tempFile.Close()

	_, err = io.Copy(tempFile, file)
	if err != nil {
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}

	// Reset file pointer for S3 upload
	tempFile.Seek(0, io.SeekStart)

	// Generate unique S3 key
	randomBytes := make([]byte, 16)
	rand.Read(randomBytes)
	fileKey := fmt.Sprintf("videos/%s.mp4", hex.EncodeToString(randomBytes))

	// Upload to S3
	contentType := "video/mp4"
	_, err = cfg.s3Client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(fileKey),
		Body:        tempFile,
		ContentType: aws.String(contentType),
	})
	
	if err != nil {
		http.Error(w, "Failed to upload to S3", http.StatusInternalServerError)
		return
	}

	// Construct S3 URL
	s3URL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, fileKey)

	// Update video record url in DB
	video.VideoURL = &s3URL
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		http.Error(w, "Failed to update database", http.StatusInternalServerError)
		return
	}

	// Return success response
	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"message": "Video uploaded successfully", "s3_url": "%s"}`, s3URL)
}
