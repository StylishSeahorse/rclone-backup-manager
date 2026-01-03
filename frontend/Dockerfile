FROM node:18-alpine AS builder

WORKDIR /app

# Copy package files
COPY package*.json ./

# Install dependencies
RUN npm install

# Copy all source files explicitly
COPY index.html ./
COPY vite.config.js ./
COPY nginx.conf ./
COPY src ./src

# Verify files exist before build (debug step)
RUN ls -la src/components/

# Build
RUN npm run build

# Production nginx stage
FROM nginx:alpine

COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]