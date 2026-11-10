// Конвейер CI/CD: сборка образа tasks-service и выкладка в кластер Kubernetes (Minikube).
pipeline {
    agent any

    options {
        disableConcurrentBuilds()
        buildDiscarder(logRotator(numToKeepStr: '10'))
    }

    triggers {
        // Jenkins недоступен из интернета, поэтому вместо webhook — опрос репозитория раз в 2 минуты.
        pollSCM('H/2 * * * *')
    }

    environment {
        IMAGE     = 'tasks-service'
        NAMESPACE = 'tasks'
        APP_URL   = 'http://192.168.49.2:30080'
    }

    stages {
        stage('Версия') {
            steps {
                script {
                    env.TAG = "1.0.${env.BUILD_NUMBER}-" + sh(script: 'git rev-parse --short HEAD', returnStdout: true).trim()
                }
                echo "Собираю ${env.IMAGE}:${env.TAG}"
            }
        }

        stage('Сборка образа') {
            steps {
                // go vet и тесты выполняются внутри Dockerfile: при ошибке образ не соберётся.
                sh 'docker build --build-arg VERSION=$TAG -t $IMAGE:$TAG .'
                sh 'docker images $IMAGE --format "{{.Repository}}:{{.Tag}}  {{.Size}}" | head -5'
            }
        }

        stage('Загрузка в Minikube') {
            steps {
                // Реестра образов нет: образ передаётся напрямую в containerd узла кластера.
                sh 'docker save $IMAGE:$TAG | docker exec -i minikube ctr -n k8s.io images import -'
            }
        }

        stage('Выкладка') {
            steps {
                sh 'kubectl -n $NAMESPACE set image deployment/tasks-service app=$IMAGE:$TAG'
                sh 'kubectl -n $NAMESPACE annotate deployment/tasks-service kubernetes.io/change-cause="Jenkins #$BUILD_NUMBER: $IMAGE:$TAG" --overwrite'
                sh 'kubectl -n $NAMESPACE rollout status deployment/tasks-service --timeout=180s'
            }
        }

        stage('Проверка') {
            steps {
                sh '''
                    kubectl -n $NAMESPACE get pods -l app=tasks-service -o wide
                    response=$(curl -fsS $APP_URL/health)
                    echo "$response"
                    echo "$response" | grep -q "\\"version\\":\\"$TAG\\""
                '''
            }
        }
    }

    post {
        failure {
            // Если новая версия не поднялась или не прошла проверку — возвращаем предыдущую.
            sh 'kubectl -n $NAMESPACE rollout undo deployment/tasks-service || true'
            sh 'kubectl -n $NAMESPACE rollout status deployment/tasks-service --timeout=120s || true'
        }
        success {
            sh 'kubectl -n $NAMESPACE rollout history deployment/tasks-service | tail -5'
        }
    }
}
