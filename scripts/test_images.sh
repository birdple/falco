#!/bin/bash

echo "🧪 Testing Imagine Image Processing Service with Your Files"
echo "=========================================================="

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Function to test upload and capture response
test_upload() {
    local file=$1
    local desc=$2
    
    echo -e "\n${BLUE}Testing: ${desc}${NC}"
    echo "File: ${file}"
    
    response=$(http -f POST http://localhost:8080/api/v1/upload file@"${file}" 2>/dev/null)
    
    if echo "$response" | grep -q '"success":true'; then
        echo -e "${GREEN}✅ SUCCESS${NC}"
        # Extract image ID for later use
        image_id=$(echo "$response" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
        echo "Image ID: $image_id"
        echo "$image_id" >> uploaded_images.txt
    else
        echo -e "${RED}❌ FAILED${NC}"
        echo "Error:" $(echo "$response" | grep -o '"message":"[^"]*"' | cut -d'"' -f4)
    fi
}

# Function to test URL upload
test_url_upload() {
    local url=$1
    local desc=$2
    
    echo -e "\n${BLUE}Testing: ${desc}${NC}"
    echo "URL: ${url}"
    
    response=$(http POST http://localhost:8080/api/v1/upload url="${url}" 2>/dev/null)
    
    if echo "$response" | grep -q '"success":true'; then
        echo -e "${GREEN}✅ SUCCESS${NC}"
        image_id=$(echo "$response" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
        echo "Image ID: $image_id"
        echo "$image_id" >> uploaded_images.txt
    else
        echo -e "${RED}❌ FAILED${NC}"
        echo "Error:" $(echo "$response" | grep -o '"message":"[^"]*"' | cut -d'"' -f4)
    fi
}

# Function to test image retrieval
test_retrieval() {
    local image_id=$1
    local desc=$2
    
    echo -e "\n${BLUE}Testing: ${desc}${NC}"
    echo "Image ID: ${image_id}"
    
    # Test original image
    if http GET "http://localhost:8080/api/v1/images/${image_id}" > /dev/null 2>&1; then
        echo -e "${GREEN}✅ Original image retrieval: SUCCESS${NC}"
    else
        echo -e "${RED}❌ Original image retrieval: FAILED${NC}"
    fi
    
    # Test resized image
    if http GET "http://localhost:8080/api/v1/images/${image_id}?w=300&h=200" > /dev/null 2>&1; then
        echo -e "${GREEN}✅ Resized image retrieval: SUCCESS${NC}"
    else
        echo -e "${RED}❌ Resized image retrieval: FAILED${NC}"
    fi
    
    # Test format conversion
    if http GET "http://localhost:8080/api/v1/images/${image_id}?f=jpeg" > /dev/null 2>&1; then
        echo -e "${GREEN}✅ Format conversion: SUCCESS${NC}"
    else
        echo -e "${RED}❌ Format conversion: FAILED${NC}"
    fi
}

# Clear previous results
rm -f uploaded_images.txt

echo -e "${BLUE}Step 1: Health Check${NC}"
if http GET http://localhost:8080/health > /dev/null 2>&1; then
    echo -e "${GREEN}✅ Health check: PASSED${NC}"
else
    echo -e "${RED}❌ Health check: FAILED${NC}"
    exit 1
fi

echo -e "\n${BLUE}Step 2: File Upload Tests${NC}"

# Test PNG file
test_upload "images/1757578781.png" "PNG File Upload"

# Test JPEG file
test_upload "images/1014.JPG" "JPEG File Upload"

# Test WebP file (expected to fail)
echo -e "\n${YELLOW}Testing: WebP File Upload (Expected to Fail)${NC}"
echo "File: images/8882.webp"
response=$(http -f POST http://localhost:8080/api/v1/upload file@images/8882.webp 2>/dev/null)
if echo "$response" | grep -q '"success":false'; then
    echo -e "${YELLOW}✅ EXPECTED FAILURE (WebP not supported)${NC}"
else
    echo -e "${RED}❌ UNEXPECTED SUCCESS${NC}"
fi

echo -e "\n${BLUE}Step 3: URL Upload Test${NC}"

# Test URL from img.txt
url=$(cat images/img.txt)
test_url_upload "$url" "URL Upload from img.txt"

echo -e "\n${BLUE}Step 4: Image Retrieval Tests${NC}"

# Test retrieval for uploaded images
if [ -f uploaded_images.txt ]; then
    while read -r image_id; do
        if [ -n "$image_id" ]; then
            test_retrieval "$image_id" "Image Retrieval Test"
        fi
    done < uploaded_images.txt
else
    echo -e "${YELLOW}⚠️  No images were successfully uploaded${NC}"
fi

echo -e "\n${GREEN}🎉 Testing Complete!${NC}"
echo "Check uploaded_images.txt for successfully uploaded image IDs"
echo "You can manually test retrieval with:"
echo "http GET http://localhost:8080/api/v1/images/YOUR_IMAGE_ID?w=300&h=200"
