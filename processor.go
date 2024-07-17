package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/davidbyttow/govips/v2/vips"
	"io/fs"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type ImageData struct {
	FullPath    string `json:"full_path"`
	ThumbPath   string `json:"thumb_path"`
	DisplayPath string `json:"display_path"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Tiles       string `json:"tiles,omitempty"`
	MaxWidth    int    `json:"max_width,omitempty"`
	MaxHeight   int    `json:"max_height,omitempty"`
	path        string `json:"-"`
	name        string `json:"-"`
}

var logger = log.Default()

const thumbnailHeight = 400
const slideHeight = 2000
const tileMinDimension = 4100

func main() {
	flag.Parse()
	if len(flag.Args()) != 1 {
		panic("Must provide a directory")
	}
	root := flag.Args()[0]

	var imageDataMap = map[string]map[string]*ImageData{}
	var imageDataChan = make(chan *ImageData, 100)

	logger.Printf("Building image file list...")
	err := buildImageList(root, imageDataMap, imageDataChan)
	if err != nil {
		return
	}

	vips.Startup(nil)
	vips.LoggingSettings(nil, vips.LogLevelMessage)
	defer vips.Shutdown()

	for imageData := range imageDataChan {
		logger.Println(imageData.path)

		processImage(imageData)

		imageDataMap[filepath.Dir(imageData.path)][imageData.name] = imageData
	}

	for dir, imageData := range imageDataMap {
		writeDirImageData(dir, imageData)
	}
}

func buildImageList(root string, imageDataMap map[string]map[string]*ImageData, imageDataChan chan *ImageData) error {
	skipFileNames := []string{".DS_Store", "thumbnail", "display", "html", "dzi", "json", "xml"}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		for _, skipFileName := range skipFileNames {
			// don't process non-images or already generated images
			if strings.Contains(d.Name(), skipFileName) {
				return nil
			}
		}

		if d.IsDir() {
			// skip dz tiles generated externally or previously
			if strings.HasSuffix(d.Name(), "_files") {
				return filepath.SkipDir
			}

			// new directory, create image data sub map
			if _, exists := imageDataMap[path]; !exists {
				imageDataMap[path] = map[string]*ImageData{}
			}

			// nothing else to do with directories
			return nil
		} else {
			ext := filepath.Ext(d.Name())
			name := strings.TrimSuffix(d.Name(), ext)

			var imageData = ImageData{
				path: path,
				name: name,
			}

			imageDataChan <- &imageData
		}

		return nil
	})
	if err != nil {
		fmt.Println(err)
		return err
	}
	close(imageDataChan)
	return nil
}

func convertToJPG(imageData *ImageData, image *vips.ImageRef) error {
	ext := ".jpg"
	// vips image to jpg
	imageFile := fmt.Sprintf("%s%s", imageData.name, ext)
	path := filepath.Join(imageData.FullPath, imageFile)

	// for web viewing/consistency with generated tiles
	err := image.ToColorSpace(vips.InterpretationSRGB)
	if err != nil {
		return err
	}

	jpgImageBytes, _, err := image.ExportJpeg(vips.NewJpegExportParams())
	if err != nil {
		return err
	}

	err = os.WriteFile(path, jpgImageBytes, 0644)
	if err != nil {
		return err
	}
	return nil
}

func processImage(imageData *ImageData) {
	jpgExportParams := &vips.JpegExportParams{
		StripMetadata:      true,
		Quality:            75,
		Interlace:          true,
		OptimizeCoding:     true,
		SubsampleMode:      vips.VipsForeignSubsampleAuto,
		TrellisQuant:       true,
		OvershootDeringing: true,
		OptimizeScans:      true,
		QuantTable:         3,
	}

	dir := filepath.Dir(imageData.path)

	image, err := vips.NewImageFromFile(imageData.path)
	defer image.Close()
	if err != nil {
		panic(err)
	}

	// png is nice but way too big
	if filepath.Ext(imageData.path) == ".png" {
		logger.Printf("Retyping image to jpg: %s", imageData.path)

		err := convertToJPG(imageData, image)
		if err != nil {
			panic(err)
		}
	}

	ext := ".jpg"
	imageData.ThumbPath = filepath.Join(dir, imageData.name+"-thumbnail"+ext)
	imageData.DisplayPath = filepath.Join(dir, imageData.name+"-display"+ext)
	imageData.FullPath = filepath.Join(dir, imageData.name+ext)

	imageData.Height = image.Height()
	imageData.MaxHeight = image.Height()

	imageData.Width = image.Width()
	imageData.MaxWidth = image.Width()

	// the grid thumbnail
	err = generateThumbnail(imageData, jpgExportParams)
	if err != nil {
		panic(err)
	}

	// the slide image
	if image.Width() > slideHeight || image.Height() > slideHeight {
		err = generateSlideImage(imageData, jpgExportParams)
		if err != nil {
			panic(err)
		}
	}

	// generate tiles if necessary
	if image.Width() > tileMinDimension || image.Height() > tileMinDimension {
		generateImageTiles(imageData)
	}
}

func generateThumbnail(imageData *ImageData, jpgExportParams *vips.JpegExportParams) error {
	thumbnail, err := vips.NewThumbnailFromFile(imageData.FullPath, math.MaxInt16, thumbnailHeight, vips.InterestingNone)
	if err != nil {
		return err
	}
	defer thumbnail.Close()

	thumbnailBytes, _, err := thumbnail.ExportJpeg(jpgExportParams)
	if err != nil {
		return err
	}
	err = os.WriteFile(imageData.ThumbPath, thumbnailBytes, 0644)
	if err != nil {
		return err
	}

	return nil
}

func generateSlideImage(imageData *ImageData, jpgExportParams *vips.JpegExportParams) error {
	display, err := vips.NewThumbnailFromFile(imageData.FullPath, math.MaxInt16, slideHeight, vips.InterestingNone)
	if err != nil {
		return err
	}
	defer display.Close()

	displayBytes, _, err := display.ExportJpeg(jpgExportParams)
	if err != nil {
		return err
	}

	err = os.WriteFile(imageData.DisplayPath, displayBytes, 0644)
	if err != nil {
		return err
	}

	imageData.Height = display.Height()
	imageData.Width = display.Width()
	return nil
}

func generateImageTiles(imageData *ImageData) {
	logger.Printf("Generating tiles for %s", imageData.path)

	// Shell out because govips doesn't have a dzsave binding
	imageBaseDir := filepath.Join(filepath.Dir(imageData.path), imageData.name)
	vipsDzCmd := exec.Command("vips", "dzsave", imageData.path, imageBaseDir, "--centre")
	err := vipsDzCmd.Run()
	if err != nil {
		panic(err)
	}

	imageData.Tiles = imageBaseDir + "_files"

	// delete the unnecessary generated meta files
	err = os.Remove(imageBaseDir + ".dzi")
	if err != nil {
		logger.Println(err)
	}
}

func writeDirImageData(dir string, imageData map[string]*ImageData) {
	logger.Printf("Writing JSON to %s/images.json", dir)

	logger.Printf("Opening JSON file %s", dir)
	jsonFile, err := os.Create(filepath.Join(dir, "images.json"))

	defer func() {
		logger.Printf("Closing JSON file for %s", dir)
		err := jsonFile.Close()
		if err != nil {
			logger.Println(err)
			return
		}
	}()

	imageJson, err := json.MarshalIndent(imageData, "", "  ")
	if err != nil {
		panic(err)
	}
	_, err = jsonFile.Write(imageJson)
	if err != nil {
		panic(err)
	}
}
